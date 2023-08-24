package main

import (
	"bytes"
	//"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	tele "gopkg.in/telebot.v3"
)

// [ ] TODO: Start dialog with short instructions on how to use chat commands
// [ ] TODO: Handle SIGINT and do graceful shutdown
// [ ] TODO: Do not os.Exit() or log.Fatal or panic!
// [*] TODO: Balancer between instances
// [*] TODO: Sticky sessions within instances
// [*] TODO: PRO / Chat selector
// [*] TODO: Session reset?
// [*] DONE: Do not send next requests while the first one is not processed? Or allow parallel inference of different messages?

var (
	mu sync.Mutex // Global mutex TODO: Implement better solutions

	chatZoo []string
	proZoo  []string
	zoo     map[string][]string
)

type Job struct {
	ID     string `json: "id"`
	Prompt string `json: "prompt"`
	Output string `json: "output"`
	Status string `json: "status"`
}

type User struct {
	ID        string // User ID within external system
	TGID      int64  // User ID within Telegram
	Mode      string // pro / chat
	SessionID string // current session
	Status    string
	Server    string // Server address for sticky sessions
}

type Session struct {
	UserID    string // User ID within external system
	TGID      string // User ID within Telegram
	SessionID string // Unique UUID v4
	Prompts   []string
	Outputs   []string
	Status    string
	Server    string // Server address for sticky sessions
}

var (
	users    map[int64]*User
	sessions map[string]string
)

func init() {
	users = make(map[int64]*User)
	sessions = make(map[string]string)
	zoo = make(map[string][]string)
}

func main() {

	fmt.Printf("\nTeleZoo v0.4 is starting...")

	// -- Read settings and init all

	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	chatZoo = strings.Split(os.Getenv("CHATZOO"), ",")
	proZoo = strings.Split(os.Getenv("PROZOO"), ",")
	zoo["chat"] = chatZoo
	zoo["pro"] = chatZoo

	pref := tele.Settings{
		Token:  os.Getenv("TELEGRAM_TOKEN"),
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	}

	// -- Set up bot

	bot, err := tele.NewBot(pref)
	if err != nil {
		log.Fatal(err)
		return
	}

	// -- Handle user messages [ that weren't captured by other handlers ]

	bot.Handle(tele.OnText, func(c tele.Context) error {

		tgUser := c.Sender()
		prompt := c.Text()

		fmt.Printf("\n\nNEW REQ: %+v", prompt)

		var user *User
		var ok bool

		// -- new user ?

		mu.Lock()
		if user, ok = users[tgUser.ID]; !ok {
			fmt.Printf("\n\nNEW USER: %d", tgUser.ID) // DEBUG
			user = &User{
				ID:        "",
				TGID:      tgUser.ID,
				Mode:      "chat",
				Server:    zoo["chat"][rand.Intn(len(chatZoo))],
				SessionID: uuid.New().String(),
				Status:    "",
			}
			users[tgUser.ID] = user
		}
		mu.Unlock()

		// -- catch processing GPU slot for the current request, or wait while it will be available
		//    this allows to process multiple DDoS requests from the same users one by one
		//    TODO: wathcdog / deadline to break deadlocks

		breakLoop := false
		for {
			mu.Lock()
			if user.Status != "processing" {
				user.Status = "processing"
				breakLoop = true
			}
			mu.Unlock()
			if breakLoop {
				break
			}
			//fmt.Printf(" [ WAIT-FOR-GPU-SLOT ] ") // DEBUG
			time.Sleep(200 * time.Millisecond)
		}

		id := uuid.New().String()

		body := "{ \"id\": \"" + id + "\", \"session\": \"" + user.SessionID + "\", \"prompt\": \"" + prompt + "\" }"
		bodyReader := bytes.NewReader([]byte(body))

		fmt.Printf("\n\nREQ: %s", body)

		url := /*os.Getenv("FAST")*/ user.Server + "/jobs"
		req, err := http.NewRequest(http.MethodPost, url, bodyReader)
		if err != nil {
			fmt.Printf("\n[ERR] HTTP POST: could not create request: %s\n", err)
			os.Exit(1) // FIXME
		}
		req.Header.Set("Content-Type", "application/json")

		client := http.Client{
			Timeout: 3 * time.Second,
		}

		res, err := client.Do(req)
		if err != nil {
			fmt.Printf("\n[ERR] HTTP: error making http request: %s\n", err)
			os.Exit(1) // FIXME
		}
		defer res.Body.Close()

		//fmt.Printf("\n\n%+v", res)
		//fmt.Printf("\n\n%+v", res.Body)

		url = /*os.Getenv("FAST")*/ user.Server + "/jobs/" + id
		//fmt.Printf("\n ===> %s", url)

		req, err = http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			fmt.Printf("\n[ERR] HTTP: could not create request: %s\n", err)
			os.Exit(1) // FIXME
		}
		req.Header.Set("Content-Type", "application/json")

		var job Job
		var msg *tele.Message
		for job.Status != "finished" {
			// TODO: Better and robust handling with error checking and deadlines

			//client := http.Client{
			//    Timeout: 30 * time.Second,
			//}

			// TODO: Error Handling
			res, err := client.Do(req)
			if err != nil {
				fmt.Printf("\n[ERR] HTTP GET: could not create request: %s\n", err)
				os.Exit(1) // FIXME
			}

			output, err := io.ReadAll(res.Body)

			//fmt.Printf("\n=> %+v", string(output))

			json.Unmarshal(output, &job) // TODO: Error Handling

			if msg == nil {
				msg, _ = bot.Send(tgUser /*string(output)*/, job.Output)
			} else {
				bot.Edit(msg, job.Output)
			}

			//fmt.Printf("\n=> %+v", job)

			//body := "{\"id\": \"" + user.SessionID + "\", \"prompt\": \"" + prompt + "\"}"
			//bodyReader := bytes.NewReader([]byte(body))

			//fmt.Printf(" [ WAIT-WHILE-REQ-PROCESSED ] ") // DEBUG
			time.Sleep(200 * time.Millisecond)

		}

		//return c.Send(string(job.Output))

		fmt.Printf("\n\nFINISHED")

		mu.Lock()
		user.Status = "" // TODO: Enum all statuses and flow between them
		mu.Unlock()

		return nil
	})
	/*
		bot.Handle(tele.OnQuery, func(c tele.Context) error {

			results := make(tele.Results, 1, 1) // []tele.Result
			result := &tele.PhotoResult{
				URL:      "https://image.jpg",
				ThumbURL: "https://thumb.jpg", // required for photos
			}
			results[0] = result
			// Incoming inline queries.
			return c.Answer(
				&tele.QueryResponse{
					Results: results,
				})
		})
	*/

	// -- Start new session

	bot.Handle("/new", func(c tele.Context) error {
		tgUser := c.Sender()

		mu.Lock()
		if user, ok := users[tgUser.ID]; ok {
			fmt.Printf("\n\nNEW SESSION") // DEBUG
			user.Server = zoo[user.Mode][rand.Intn(len(chatZoo))]
			user.SessionID = uuid.New().String()
		}
		// FIXME: What if there no such user? After server restart, etc
		mu.Unlock()

		return c.Send("Starting new chat session...")
	})

	// -- Switch into the PRO mode

	bot.Handle("/pro", func(c tele.Context) error {
		tgUser := c.Sender()

		mu.Lock()
		if user, ok := users[tgUser.ID]; ok {
			user.Mode = "pro"
			user.Server = zoo[user.Mode][rand.Intn(len(chatZoo))]
			user.SessionID = uuid.New().String()
		}
		// FIXME: What if there no such user? After server restart, etc
		mu.Unlock()

		return c.Send("Switching to PRO mode...")
	})

	// -- Switch into the CHAT mode

	bot.Handle("/chat", func(c tele.Context) error {
		tgUser := c.Sender()

		mu.Lock()
		if user, ok := users[tgUser.ID]; ok {
			user.Mode = "chat"
			user.Server = zoo[user.Mode][rand.Intn(len(chatZoo))]
			user.SessionID = uuid.New().String()
		}
		// FIXME: What if there no such user? After server restart, etc
		mu.Unlock()

		return c.Send("Switching to CHAT mode...")
	})

	fmt.Printf("\nListen for Telegram...")
	bot.Start()
}
