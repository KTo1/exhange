package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/KTo1/exhange/pkg/protocol"
)

type Client struct {
	conn        net.Conn
	currentRoom string
	rooms       map[string]string // name -> id
	username    string
	welcomeText string
}

func main() {
	addr := flag.String("addr", "", "адрес сервера (напр. 192.168.1.10:9000)")
	flag.Parse()

	if *addr == "" {
		*addr = os.Getenv("EXHANGE_ADDR")
	}
	if *addr == "" {
		*addr = "localhost:9000"
	}

	fmt.Printf("подключение к %s...\n", *addr)
	conn, err := net.Dial("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "не могу подключиться к %s: %v\n", *addr, err)
		fmt.Fprintf(os.Stderr, "укажите адрес через -addr или переменную EXHANGE_ADDR\n")
		os.Exit(1)
	}
	fmt.Printf("подключено к %s\n", *addr)
	defer conn.Close()

	c := &Client{
		conn:  conn,
		rooms: make(map[string]string),
	}

	if !c.authenticate() {
		return
	}

	go c.readLoop()

	c.inputLoop()
}

func (c *Client) authenticate() bool {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Print("Логин или регистрация? (login/register): ")
	scanner.Scan()
	action := strings.TrimSpace(scanner.Text())
	if action != "login" && action != "register" {
		fmt.Println("введите login или register")
		return false
	}

	fmt.Print("Имя пользователя: ")
	scanner.Scan()
	username := strings.TrimSpace(scanner.Text())
	if username == "" {
		fmt.Println("имя пользователя не может быть пустым")
		return false
	}

	fmt.Print("Пароль: ")
	scanner.Scan()
	password := strings.TrimSpace(scanner.Text())

	req := protocol.LoginRequest{
		Type:     action,
		Username: username,
		Password: password,
	}
	protocol.WriteMessage(c.conn, &req)

	// ждем ответ сервера
	reader := bufio.NewReader(c.conn)
	meta, raw, err := protocol.ReadMessage(reader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "соединение разорвано: %v\n", err)
		return false
	}

	switch meta.Type {
	case "welcome":
		var w protocol.Welcome
		json.Unmarshal(raw, &w)
		c.username = username
		c.welcomeText = w.Text
		fmt.Println(w.Text)
		if len(w.Users) > 0 {
			fmt.Printf("\n*** пользователи в сети: %v ***\n", w.Users)
		}
		return true
	case "error":
		var e protocol.ErrorMsg
		json.Unmarshal(raw, &e)
		fmt.Fprintf(os.Stderr, "*** ошибка: %s ***\n", e.Text)
		return false
	default:
		fmt.Fprintf(os.Stderr, "*** неожиданный ответ сервера: %s ***\n", meta.Type)
		return false
	}
}

func (c *Client) inputLoop() {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Print("> ")

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			fmt.Print("> ")
			continue
		}

		if line == "/quit" {
			return
		}

		c.handleInput(line)
		fmt.Print("> ")
	}
}

func (c *Client) handleInput(line string) {
	switch {
	case line == "/users":
		protocol.WriteMessage(c.conn, &protocol.ListRequest{Type: "list_users"})

	case line == "/help":
		fmt.Println(c.welcomeText)
	case line == "/rooms":
		protocol.WriteMessage(c.conn, &protocol.ListRequest{Type: "list_rooms"})

	case strings.HasPrefix(line, "/create "):
		args := parseArgs(line[len("/create "):])
		if len(args) < 2 {
			fmt.Println("*** использование: /create <название> <участник1> [участник2 ...] ***")
			return
		}
		name := args[0]
		members := args[1:]
		req := protocol.CreateRoomRequest{
			Type:    "create_room",
			Name:    name,
			Members: members,
		}
		protocol.WriteMessage(c.conn, &req)

	case strings.HasPrefix(line, "/switch "):
		name := strings.TrimSpace(line[len("/switch "):])
		if _, ok := c.rooms[name]; ok {
			c.currentRoom = name
			fmt.Printf("*** текущая комната: %s ***\n", name)
		} else {
			fmt.Printf("*** комната %q не найдена ***\n", name)
		}

	case strings.HasPrefix(line, "/msg "):
		args := parseMsgArgs(line[len("/msg "):])
		if len(args) < 2 {
			fmt.Println("*** использование: /msg <комната> <текст> ***")
			return
		}
		roomName := args[0]
		text := args[1]
		roomID, ok := c.rooms[roomName]
		if !ok {
			fmt.Printf("*** комната %q не найдена ***\n", roomName)
			return
		}
		req := protocol.SendMessageRequest{
			Type:   "send_message",
			RoomID: roomID,
			Text:   text,
		}
		protocol.WriteMessage(c.conn, &req)

	default:
		if strings.HasPrefix(line, "/") {
			fmt.Println("*** неизвестная команда ***")
			return
		}
		// plain text -> send to current room
		if c.currentRoom == "" {
			fmt.Println("*** не выбрана комната. Используйте /switch <комната> ***")
			return
		}
		roomID, ok := c.rooms[c.currentRoom]
		if !ok {
			fmt.Println("*** текущая комната не найдена. Проверьте /rooms ***")
			return
		}
		req := protocol.SendMessageRequest{
			Type:   "send_message",
			RoomID: roomID,
			Text:   line,
		}
		protocol.WriteMessage(c.conn, &req)
	}
}

func (c *Client) readLoop() {
	reader := bufio.NewReader(c.conn)
	for {
		meta, raw, err := protocol.ReadMessage(reader)
		if err != nil {
			if err != io.EOF {
				fmt.Fprintf(os.Stderr, "\n*** соединение разорвано: %v ***\n", err)
			}
			os.Exit(0)
		}

		switch meta.Type {
		case "user_joined":
			var msg protocol.UserJoined
			json.Unmarshal(raw, &msg)
			fmt.Printf("\n*** %s вошел в сеть ***\n> ", msg.Username)

		case "user_left":
			var msg protocol.UserLeft
			json.Unmarshal(raw, &msg)
			fmt.Printf("\n*** %s вышел из сети ***\n> ", msg.Username)

		case "room_created":
			var msg protocol.RoomCreated
			json.Unmarshal(raw, &msg)
			c.rooms[msg.Name] = msg.RoomID
			fmt.Printf("\n*** комната %q создана (участники: %v) ***\n> ", msg.Name, msg.Members)
			if c.currentRoom == "" {
				c.currentRoom = msg.Name
			}

		case "new_message":
			var msg protocol.NewMessage
			json.Unmarshal(raw, &msg)
			roomName := c.roomName(msg.RoomID)
			fmt.Printf("\n[%s] %s: %s\n> ", roomName, msg.From, msg.Text)

		case "user_list":
			var msg protocol.UserList
			json.Unmarshal(raw, &msg)
			fmt.Printf("\n*** в сети: %v ***\n> ", msg.Users)

		case "room_list":
			var msg protocol.RoomList
			json.Unmarshal(raw, &msg)
			if len(msg.Rooms) == 0 {
				fmt.Printf("\n*** у вас нет комнат ***\n> ")
			} else {
				fmt.Println()
				for _, r := range msg.Rooms {
					c.rooms[r.Name] = r.ID
					fmt.Printf("  [%s] %s — участники: %v\n", r.ID, r.Name, r.Members)
				}
				fmt.Print("> ")
			}

		case "error":
			var msg protocol.ErrorMsg
			json.Unmarshal(raw, &msg)
			fmt.Printf("\n*** ошибка: %s ***\n> ", msg.Text)
		}
	}
}

func (c *Client) roomName(id string) string {
	for name, rid := range c.rooms {
		if rid == id {
			return name
		}
	}
	return id
}

func parseArgs(input string) []string {
	var parts []string
	inQuote := false
	current := ""

	for _, ch := range input {
		switch ch {
		case '"':
			inQuote = !inQuote
		case ' ':
			if inQuote {
				current += string(ch)
			} else if current != "" {
				parts = append(parts, current)
				current = ""
			}
		default:
			current += string(ch)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

// parseMsgArgs разбирает /msg <комната> <текст>, где текст это всё после названия комнаты
func parseMsgArgs(input string) []string {
	input = strings.TrimSpace(input)
	idx := strings.IndexByte(input, ' ')
	if idx < 0 {
		return []string{input}
	}
	room := input[:idx]
	text := strings.TrimSpace(input[idx+1:])
	if text == "" {
		return []string{room}
	}
	return []string{room, text}
}
