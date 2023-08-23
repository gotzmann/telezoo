package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	tele "gopkg.in/telebot.v3"
)

// [ ] TODO: Balancer between instances
// [ ] TODO: Sticky sessions within instances
// [ ] TODO: PRO / Chat selector
// [ ] TODO: Session reset?
// [+] DONE: Do not send next requests while the first one is not processed? Or allow parallel inference of different messages?

var mu sync.Mutex // Global mutex TODO: Implement better solutions

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
}

type Session struct {
	UserID    string // User ID within external system
	TGID      string // User ID within Telegram
	SessionID string // Unique UUID v4
	Prompts   []string
	Outputs   []string
	Status    string
}

var (
	users    map[int64]*User
	sessions map[string]string
)

func init() {
	users = make(map[int64]*User)
	sessions = make(map[string]string)
}

func main() {

	fmt.Printf("\nTeleZoo v0.2 is starting...")

	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	pref := tele.Settings{
		Token:  os.Getenv("TELEGRAM_TOKEN"),
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	}

	bot, err := tele.NewBot(pref)
	if err != nil {
		log.Fatal(err)
		return
	}

	bot.Handle("/pro", func(c tele.Context) error {
		return c.Send("Switched to PRO mode...")
	})

	bot.Handle("/chat", func(c tele.Context) error {
		return c.Send("Switched to CHAT mode...")
	})

	// -- All the text messages that weren't captured by existing handlers

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

		//res, err := http.Get(requestURL)
		//if err != nil {
		//    fmt.Printf("error making http request: %s\n", err)
		//    os.Exit(1)
		//}

		id := uuid.New().String()

		body := "{ \"id\": \"" + id + "\", \"session\": \"" + user.SessionID + "\", \"prompt\": \"" + prompt + "\" }"
		bodyReader := bytes.NewReader([]byte(body))

		fmt.Printf("\n\nREQ: %s", body)

		//jsonBody := []byte(`{"id": "` + user.SessionID + `", "prompt": "` + prompt + `"}`)
		//bodyReader := bytes.NewReader(jsonBody)

		url := os.Getenv("FAST") + "/jobs"
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

		//output, err := io.ReadAll(res.Body)
		// b, err := ioutil.ReadAll(resp.Body)  Go.1.15 and earlier
		//if err != nil {
		//	log.Fatalln(err)
		//}

		// Instead, prefer a context short-hand:
		//id := fmt.Sprintf("%d", user.TGID)
		//return c.Send("LANG: " + user.LanguageCode + " USER: " + id + " | " + user.Username + " TEXT: " + text)

		url = os.Getenv("FAST") + "/jobs/" + id
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

	bot.Handle(tele.OnQuery, func(c tele.Context) error {
		//var (
		//	user = c.Sender()
		//	text = c.Text()
		//)

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

	fmt.Printf("\nListen for Telegram...")
	bot.Start()
}
