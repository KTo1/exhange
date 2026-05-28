package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"bufio"

	"golang.org/x/crypto/bcrypt"

	"github.com/KTo1/exhange/pkg/protocol"
)

type User struct {
	username      string
	authenticated bool
	sendCh        chan any
}

type Room struct {
	id      string
	name    string
	members map[string]bool
}

type Hub struct {
	accounts  map[string]string // username -> bcrypt hash
	conns     map[string][]*User // username -> active connections
	rooms     map[string]*Room
	userRooms map[string][]string // username -> []roomID

	commands chan hubCommand
}

type hubCommand struct {
	user    *User
	payload any
}

type disconnectCmd struct{}

func NewHub() *Hub {
	h := &Hub{
		accounts:  make(map[string]string),
		conns:     make(map[string][]*User),
		rooms:     make(map[string]*Room),
		userRooms: make(map[string][]string),
		commands:  make(chan hubCommand, 256),
	}
	h.loadAccounts()
	return h
}

func (h *Hub) loadAccounts() {
	data, err := os.ReadFile("users.json")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		log.Printf("ошибка чтения users.json: %v", err)
		return
	}
	if err := json.Unmarshal(data, &h.accounts); err != nil {
		log.Printf("ошибка парсинга users.json: %v", err)
		return
	}
	log.Printf("загружено %d аккаунтов из users.json", len(h.accounts))
}

func (h *Hub) saveAccounts() {
	data, err := json.MarshalIndent(h.accounts, "", "  ")
	if err != nil {
		log.Printf("ошибка сериализации аккаунтов: %v", err)
		return
	}
	if err := os.WriteFile("users.json", data, 0600); err != nil {
		log.Printf("ошибка записи users.json: %v", err)
	}
}

func (h *Hub) loadMOTD() string {
	data, err := os.ReadFile("motd.txt")
	if err != nil {
		return defaultMOTD()
	}
	text := string(data)
	if text == "" {
		return defaultMOTD()
	}
	return text
}

func defaultMOTD() string {
	return fmt.Sprintf(`========================================
  ДОБРО ПОЖАЛОВАТЬ В МЕССЕНДЖЕР
========================================

ВАЖНО: чтобы отправлять сообщения, нужно сначала создать комнату
или чтобы кто-то создал комнату с вами.

КОМАНДЫ:

  /users
      Показать список пользователей, которые сейчас в сети.

  /rooms
      Показать список комнат, в которых вы состоите.
      Для каждой комнаты выводится её ID, название и участники.

  /create <название> <участник1> [участник2 ...]
      Создать комнату и пригласить в неё участников.
      Вы автоматически становитесь участником.
      Пример:
        /create друзья alice bob
        /create "общий чат" alice bob charlie

  /switch <комната>
      Выбрать комнату для отправки сообщений.
      После этого можно писать просто текст, без /msg.
      Пример:
        /switch друзья
        привет всем!         <-- отправится в комнату "друзья"

  /msg <комната> <текст>
      Отправить сообщение в комнату без переключения на неё.
      Пример:
        /msg друзья привет, как дела?

  /help
      Показать эту справку.

  /quit
      Выйти из мессенджера.`)
}

func (h *Hub) Run() {
	for cmd := range h.commands {
		switch p := cmd.payload.(type) {
		case *protocol.LoginRequest:
			if p.Type == "register" {
				h.handleRegister(cmd.user, p)
			} else {
				h.handleLogin(cmd.user, p)
			}
		case *protocol.CreateRoomRequest:
			h.handleCreateRoom(cmd.user, p)
		case *protocol.SendMessageRequest:
			h.handleSendMessage(cmd.user, p)
		case *protocol.ListRequest:
			h.handleListRequest(cmd.user, p)
		case disconnectCmd:
			h.handleDisconnect(cmd.user)
		}
	}
}

func (h *Hub) handleLogin(user *User, req *protocol.LoginRequest) {
	hash, exists := h.accounts[req.Username]
	if !exists {
		user.sendCh <- protocol.ErrorMsg{Type: "error", Text: "пользователь не существует, используйте /register"}
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		user.sendCh <- protocol.ErrorMsg{Type: "error", Text: "неверный пароль"}
		return
	}
	if _, online := h.users[req.Username]; online {
		user.sendCh <- protocol.ErrorMsg{Type: "error", Text: "пользователь уже в сети"}
		return
	}

	user.username = req.Username
	user.authenticated = true
	h.users[req.Username] = user

	online := h.onlineUsers()
	user.sendCh <- protocol.Welcome{
		Type:  "welcome",
		Text:  h.loadMOTD(),
		Users: online,
	}

	h.broadcastExcept(req.Username, protocol.UserJoined{Type: "user_joined", Username: req.Username})
	log.Printf("[+] %s вошел в сеть (онлайн: %d)", req.Username, len(h.users))
}

func (h *Hub) handleRegister(user *User, req *protocol.LoginRequest) {
	if _, exists := h.accounts[req.Username]; exists {
		user.sendCh <- protocol.ErrorMsg{Type: "error", Text: "пользователь уже существует"}
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		user.sendCh <- protocol.ErrorMsg{Type: "error", Text: "внутренняя ошибка"}
		return
	}

	h.accounts[req.Username] = string(hash)
	h.saveAccounts()

	user.username = req.Username
	user.authenticated = true
	h.users[req.Username] = user

	online := h.onlineUsers()
	user.sendCh <- protocol.Welcome{
		Type:  "welcome",
		Text:  h.loadMOTD(),
		Users: online,
	}

	h.broadcastExcept(req.Username, protocol.UserJoined{Type: "user_joined", Username: req.Username})
	log.Printf("[+] %s зарегистрировался и вошел в сеть (онлайн: %d)", req.Username, len(h.users))
}

func (h *Hub) handleCreateRoom(user *User, req *protocol.CreateRoomRequest) {
	if !h.requireAuth(user) {
		return
	}

	id := generateID()
	room := &Room{
		id:      id,
		name:    req.Name,
		members: make(map[string]bool),
	}
	room.members[user.username] = true
	var memberList []string
	memberList = append(memberList, user.username)

	for _, m := range req.Members {
		if m == user.username {
			continue
		}
		room.members[m] = true
		memberList = append(memberList, m)
	}

	h.rooms[id] = room

	created := protocol.RoomCreated{
		Type:    "room_created",
		RoomID:  id,
		Name:    req.Name,
		Members: memberList,
	}

	for _, m := range memberList {
		h.userRooms[m] = append(h.userRooms[m], id)
		if u, ok := h.users[m]; ok {
			u.sendCh <- created
		}
	}

	log.Printf("[room] %s создал комнату %q (%s) с участниками: %v", user.username, req.Name, id, memberList)
}

func (h *Hub) handleSendMessage(user *User, req *protocol.SendMessageRequest) {
	if !h.requireAuth(user) {
		return
	}

	room, ok := h.rooms[req.RoomID]
	if !ok {
		user.sendCh <- protocol.ErrorMsg{Type: "error", Text: "комната не найдена"}
		return
	}
	if !room.members[user.username] {
		user.sendCh <- protocol.ErrorMsg{Type: "error", Text: "вы не участник этой комнаты"}
		return
	}

	msg := protocol.NewMessage{
		Type:   "new_message",
		RoomID: req.RoomID,
		From:   user.username,
		Text:   req.Text,
	}
	for m := range room.members {
		if u, ok := h.users[m]; ok {
			u.sendCh <- msg
		}
	}
}

func (h *Hub) handleListRequest(user *User, req *protocol.ListRequest) {
	if !h.requireAuth(user) {
		return
	}

	switch req.Type {
	case "list_users":
		user.sendCh <- protocol.UserList{Type: "user_list", Users: h.onlineUsers()}
	case "list_rooms":
		rooms := h.roomsForUser(user.username)
		user.sendCh <- protocol.RoomList{Type: "room_list", Rooms: rooms}
	}
}

func (h *Hub) handleDisconnect(user *User) {
	if !user.authenticated {
		return
	}
	delete(h.users, user.username)
	h.broadcastAll(protocol.UserLeft{Type: "user_left", Username: user.username})
	log.Printf("[-] %s вышел из сети (онлайн: %d)", user.username, len(h.users))
}

func (h *Hub) requireAuth(user *User) bool {
	if !user.authenticated {
		user.sendCh <- protocol.ErrorMsg{Type: "error", Text: "сначала войдите в систему"}
		return false
	}
	return true
}

func (h *Hub) onlineUsers() []string {
	var list []string
	for u := range h.users {
		list = append(list, u)
	}
	return list
}

func (h *Hub) roomsForUser(username string) []protocol.RoomInfo {
	var list []protocol.RoomInfo
	for _, rid := range h.userRooms[username] {
		if r, ok := h.rooms[rid]; ok {
			var members []string
			for m := range r.members {
				members = append(members, m)
			}
			list = append(list, protocol.RoomInfo{ID: r.id, Name: r.name, Members: members})
		}
	}
	return list
}

func (h *Hub) broadcastExcept(username string, msg any) {
	for name, u := range h.users {
		if name != username {
			u.sendCh <- msg
		}
	}
}

func (h *Hub) broadcastAll(msg any) {
	for _, u := range h.users {
		u.sendCh <- msg
	}
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// --- Обработка соединения ---

func handleConn(conn net.Conn, hub *Hub) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	user := &User{
		sendCh: make(chan any, 64),
	}

	// write goroutine
	go func() {
		for msg := range user.sendCh {
			protocol.WriteMessage(conn, msg)
		}
	}()

	for {
		meta, raw, err := protocol.ReadMessage(reader)
		if err != nil {
			break
		}

		switch meta.Type {
		case "login":
			var req protocol.LoginRequest
			if json.Unmarshal(raw, &req) != nil {
				continue
			}
			hub.commands <- hubCommand{user: user, payload: &req}

		case "register":
			var req protocol.LoginRequest
			if json.Unmarshal(raw, &req) != nil {
				continue
			}
			hub.commands <- hubCommand{user: user, payload: &req}

		default:
			if !user.authenticated {
				user.sendCh <- protocol.ErrorMsg{Type: "error", Text: "сначала войдите в систему"}
				continue
			}

			switch meta.Type {
			case "create_room":
				var req protocol.CreateRoomRequest
				if json.Unmarshal(raw, &req) != nil {
					continue
				}
				hub.commands <- hubCommand{user: user, payload: &req}

			case "send_message":
				var req protocol.SendMessageRequest
				if json.Unmarshal(raw, &req) != nil {
					continue
				}
				hub.commands <- hubCommand{user: user, payload: &req}

			case "list_users", "list_rooms":
				hub.commands <- hubCommand{user: user, payload: &protocol.ListRequest{Type: meta.Type}}
			}
		}
	}

	hub.commands <- hubCommand{user: user, payload: disconnectCmd{}}
	close(user.sendCh)
}

func main() {
	hub := NewHub()
	go hub.Run()

	addr := ":9000"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("не могу слушать %s: %v", addr, err)
	}
	log.Printf("сервер запущен на %s", addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("ошибка accept: %v", err)
			continue
		}
		go handleConn(conn, hub)
	}
}
