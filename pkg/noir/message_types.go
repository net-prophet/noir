package noir

import "github.com/pion/webrtc/v3"

// Join message sent when initializing a peer connection
type Join struct {
	Sid   string                    `json:"sid"`
	Offer webrtc.SessionDescription `json:"offer"`
	Pid   string                    `json:"pid"`
}

// Negotiation message sent when renegotiating the peer connection
type Negotiation struct {
	Desc webrtc.SessionDescription `json:"desc"`
}

// Trickle message sent when renegotiating the peer connection
type Trickle struct {
	Candidate webrtc.ICECandidateInit `json:"candidate"`
	Target    int                     `json:"target"`
}

type Play struct {
	Sid      string `json:"roomID"`
	Pid      string `json:"pid"`
	Filename string `json:"filename"`
	Repeat   bool   `json:"repeat"`
}

type RPCCall struct {
	ID     string      `json:"clientID"`
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

type RPCJoin struct {
	RPCCall
	Params Join `json:"params"`
}

type RPCPlay struct {
	RPCCall
	Params Play `json:"params"`
}

type ResultOrNotify struct {
	ID         string      `json:"clientID"`
	ResultType string      `json:"type"`
	Method     string      `json:"method"`
	Params     interface{} `json:"params"`
	Result     interface{} `json:"result"`
}

type Result struct {
	ID      string      `json:"clientID"`
	Result  interface{} `json:"result"`
	JSONRPC string      `json:"jsonrpc"`
}

type Notify struct {
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
	JSONRPC string      `json:"jsonrpc"`
}
