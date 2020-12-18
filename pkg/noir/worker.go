package noir

import (
	"encoding/json"
	"github.com/go-redis/redis"
	pb "github.com/net-prophet/noir/pkg/proto"
	log "github.com/pion/ion-log"
	"github.com/pion/ion-sfu/pkg/sfu"
	"github.com/pion/webrtc/v3"
	"strings"
	"sync"
	"time"
)

const (
	RouterTopic   = "noir/"
	WebrtcTimeout = 25 * time.Second
	RouterMaxAge  = WebrtcTimeout
)

type Worker interface {
	HandleForever()
	HandleNext(timeout time.Duration) error
	GetQueue() *Queue
	ID() string
}

// worker runs 2 go threads -- Router() takes incoming router messages and loadbalances
// commands across commands queues on nodes while CommandRunner() runs commands on this node's queue
type worker struct {
	id      string
	manager *Manager
	queue   Queue
	mu      sync.RWMutex
}

func NewRedisWorkerQueue(client *redis.Client, id string) Queue {
	return NewRedisQueue(client, pb.KeyWorkerTopic(id), RouterMaxAge)
}

func NewRedisWorker(id string, manager *Manager, client *redis.Client) Worker {
	return &worker{id: id, manager: manager, queue: NewRedisWorkerQueue(client, id)}
}

func NewWorker(id string, manager *Manager, queue Queue) Worker {
	return &worker{id: id, manager: manager, queue: queue}
}

func (w *worker) HandleForever() {
	log.Debugf("worker starting on topic %s", w.queue.Topic())
	for {
		if err := w.HandleNext(0); err != nil {
			log.Errorf("worker handler error %s", err)
			time.Sleep(1 * time.Second)
		}
	}
}

func (w *worker) HandleNext(timeout time.Duration) error {
	request, err := w.NextCommand(timeout)
	if err != nil {
		return err
	}
	return w.Handle(request)
}

func (w *worker) NextCommand(timeout time.Duration) (*pb.NoirRequest, error) {
	msg, popErr := w.queue.BlockUntilNext(timeout)
	if popErr != nil {
		log.Errorf("queue error %s", popErr)
		return nil, popErr
	}

	var request pb.NoirRequest
	p_err := UnmarshalRequest(msg, &request)
	if p_err != nil {
		log.Errorf("message parse error: %s", p_err)
		return nil, p_err
	}
	return &request, nil
}

func (w *worker) ID() string {
	return w.id
}
func (w *worker) GetQueue() *Queue {
	return &w.queue
}
func (w *worker) Handle(request *pb.NoirRequest) error {
	log.Debugf("handle %s", request.Action)
	if strings.HasPrefix(request.Action, "request.signal.") {
		return w.HandleSignal(request)
	}
	if strings.HasPrefix(request.Action, "request.roomadmin.") {
		return w.HandleRoomAdmin(request)
	}
	return nil
}

func (w *worker) HandleRoomAdmin(request *pb.NoirRequest) error {
	admin := request.GetRoomAdmin()
	if request.Action == "request.roomadmin.openroom" {
		w.manager.OpenRoomFromRequest(admin)
	}
	return nil
}

func (w *worker) HandleSignal(request *pb.NoirRequest) error {
	signal := request.GetSignal()
	if request.Action == "request.signal.join" {
		return w.HandleJoin(signal)
	}
	return nil
}

func (w *worker) HandleJoin(signal *pb.SignalRequest) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	mgr := *w.manager

	peer, err := mgr.CreateClient(signal)

	if err != nil {
		return err
	}

	join := signal.GetJoin()
	pid := signal.Id

	recv := w.manager.GetQueue(pb.KeyTopicToPeer(pid))
	send := w.manager.GetQueue(pb.KeyTopicFromPeer(pid))

	log.Infof("listening on %s", recv.Topic())

	peer.OnIceCandidate = func(candidate *webrtc.ICECandidateInit, target int) {
		bytes, err := json.Marshal(candidate)
		if err != nil {
			log.Errorf("OnIceCandidate error %s", err)
		}
		err = EnqueueReply(send, &pb.NoirReply{
			Command: &pb.NoirReply_Signal{
				Signal: &pb.SignalReply{
					Id: pid,
					Payload: &pb.SignalReply_Trickle{
						Trickle: &pb.Trickle{
							Init:   string(bytes),
							Target: pb.Trickle_Target(target),
						},
					},
				},
			},
		})
		if err != nil {
			log.Errorf("OnIceCandidate send error %v ", err)
		}

	}

	peer.OnICEConnectionStateChange = func(state webrtc.ICEConnectionState) {

	}

	peer.OnOffer = func(description *webrtc.SessionDescription) {
		bytes, err := json.Marshal(description)
		if err != nil {
			log.Errorf("OnIceCandidate error %s", err)
		}
		err = EnqueueReply(send, &pb.NoirReply{
			Command: &pb.NoirReply_Signal{
				Signal: &pb.SignalReply{
					Id:      pid,
					Payload: &pb.SignalReply_Description{Description: bytes},
				},
			},
		})
		if err != nil {
			log.Errorf("OnIceCandidate send error %v ", err)
		}

	}

	var offer webrtc.SessionDescription
	offer = webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  string(join.Description),
	}

	answer, _ := peer.Join(join.Sid, offer)

	packed, _ := json.Marshal(answer)

	EnqueueReply(send, &pb.NoirReply{
		Command: &pb.NoirReply_Signal{
			Signal: &pb.SignalReply{
				Id:        pid,
				RequestId: signal.RequestId,
				Payload: &pb.SignalReply_Join{
					Join: &pb.JoinReply{
						Description: packed,
					},
				},
			},
		},
	})

	go w.PeerChannel(pid, join.Sid, peer)

	return nil
}

func (w *worker) PeerChannel(pid string, roomID string, peer *sfu.Peer) {
	recv := w.manager.GetQueue(pb.KeyTopicToPeer(pid))
	send := w.manager.GetQueue(pb.KeyTopicFromPeer(pid))
	for {
		request := pb.NoirRequest{}
		message, err := recv.BlockUntilNext(0)
		if err != nil {
			log.Errorf("getting message to peer %s", err)
		}
		err = UnmarshalRequest(message, &request)
		if err != nil {
			log.Errorf("unmarshal message to peer %s", err)
		}
		switch request.Command.(type) {
		case *pb.NoirRequest_Signal:
			signal := request.GetSignal()
			switch signal.Payload.(type) {
			case *pb.SignalRequest_Kill:
				log.Debugf("got KillRequest for peer %s", pid)
				w.manager.CloseClient(pid)
				return
			case *pb.SignalRequest_Description:
				var desc pb.Negotiation
				err := json.Unmarshal(signal.GetDescription(), &desc)
				if err != nil {
					log.Errorf("unmarshal err: %s", err)
					continue
				}
				if desc.Desc.Type == webrtc.SDPTypeAnswer {
					log.Debugf("got answer, setting description")
					peer.SetRemoteDescription(desc.Desc)
				} else if desc.Desc.Type == webrtc.SDPTypeOffer {
					roomData, err := w.manager.GetRemoteRoomData(roomID)
					if err != nil {
						log.Errorf("err getting room to validate offer: %s", err)
						continue
					}

					_, err = w.manager.ValidateOffer(roomData, pid, desc.Desc)

					if err != nil {
						log.Infof("rejected offer: %s", err)
						continue
					}

					answer, _ := peer.Answer(desc.Desc)
					bytes, err := json.Marshal(answer)
					log.Debugf("got offer, sending reply %s", string(bytes))
					err = EnqueueReply(send, &pb.NoirReply{
						Command: &pb.NoirReply_Signal{
							Signal: &pb.SignalReply{
								Id:        pid,
								RequestId: signal.RequestId,
								Payload:   &pb.SignalReply_Description{Description: bytes},
							},
						},
					})
					if err != nil {
						log.Errorf("offer answer send error %v ", err)
					}

				}
			case *pb.SignalRequest_Trickle:
				trickle := signal.GetTrickle()
				var candidate webrtc.ICECandidateInit
				err := json.Unmarshal([]byte(trickle.GetInit()), &candidate)
				if err != nil {
					log.Errorf("unmarshal err: %s %s", err, trickle.GetInit())
					continue
				}
				peer.Trickle(candidate, int(trickle.Target.Number()))
			default:
				log.Errorf("unknown signal for peer %s", signal.Payload)
			}
		default:
			log.Errorf("unknown command for peer %s", request.Command)
		}
	}
}
