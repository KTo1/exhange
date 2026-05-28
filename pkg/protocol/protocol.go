package protocol

import (
	"bufio"
	"encoding/json"
	"io"
)

type Message struct {
	Type string `json:"type"`
}

// Client -> Server
type LoginRequest struct {
	Type     string `json:"type"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type CreateRoomRequest struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

type SendMessageRequest struct {
	Type   string `json:"type"`
	RoomID string `json:"room_id"`
	Text   string `json:"text"`
}

type ListRequest struct {
	Type string `json:"type"`
}

// Server -> Client
type LoginOK struct {
	Type  string   `json:"type"`
	Users []string `json:"users"`
}

type UserJoined struct {
	Type     string `json:"type"`
	Username string `json:"username"`
}

type UserLeft struct {
	Type     string `json:"type"`
	Username string `json:"username"`
}

type RoomCreated struct {
	Type    string   `json:"type"`
	RoomID  string   `json:"room_id"`
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

type NewMessage struct {
	Type   string `json:"type"`
	RoomID string `json:"room_id"`
	From   string `json:"from"`
	Text   string `json:"text"`
}

type UserList struct {
	Type  string   `json:"type"`
	Users []string `json:"users"`
}

type RoomInfo struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

type RoomList struct {
	Type  string     `json:"type"`
	Rooms []RoomInfo `json:"rooms"`
}

type Welcome struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Users []string `json:"users"`
}

type ErrorMsg struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func ReadMessage(r *bufio.Reader) (*Message, []byte, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, nil, err
	}
	msg := &Message{}
	if err := json.Unmarshal(line, msg); err != nil {
		return nil, nil, err
	}
	return msg, line, nil
}

func WriteMessage(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}
