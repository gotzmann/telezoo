package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	tele "gopkg.in/telebot.v3"
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
}

type Session struct {
	UserID    string // User ID within external system
	TGID      string // User ID within Telegram
	SessionID string // Unique UUID v4
	Prompts   []string
	Outputs   []string
}

var (
	users    map[int64]User
	sessions map[string]string
)

func init() {
	users = make(map[int64]User)
	sessions = make(map[string]string)
}

func main() {

	fmt.Printf("\nTeleZoo v 0.0 is starting...")

	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	pref := tele.Settings{
		Token:  os.Getenv("TELEGRAM_TOKEN"),
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	}

	b, err := tele.NewBot(pref)
	if err != nil {
		log.Fatal(err)
		return
	}

	b.Handle("/pro", func(c tele.Context) error {
		return c.Send("Switched to PRO mode...")
	})

	b.Handle("/chat", func(c tele.Context) error {
		return c.Send("Switched to CHAT mode...")
	})

	b.Handle(tele.OnText, func(c tele.Context) error {
		// All the text messages that weren't
		// captured by existing handlers.

		var (
			tgUser = c.Sender()
			prompt = c.Text()
		)
		// Use full-fledged bot's functions
		// only if you need a result:
		//_, err := b.Send(tgUser, prompt)
		//if err != nil {
		//	return err
		//}
		//msg = nil

		//tgID tgUser.ID

		var user User
		var ok bool
		if user, ok = users[tgUser.ID]; !ok {
			user = User{
				ID:        "",
				TGID:      tgUser.ID,
				Mode:      "chat",
				SessionID: uuid.New().String(),
				Status:    "",
			}
			users[tgUser.ID] = user
		}

		//res, err := http.Get(requestURL)
		//if err != nil {
		//    fmt.Printf("error making http request: %s\n", err)
		//    os.Exit(1)
		//}

		body := "{\"id\": \"" + user.SessionID + "\", \"prompt\": \"" + prompt + "\"}"
		bodyReader := bytes.NewReader([]byte(body))

		fmt.Printf("\n\n%+v", body)

		//jsonBody := []byte(`{"id": "` + user.SessionID + `", "prompt": "` + prompt + `"}`)
		//bodyReader := bytes.NewReader(jsonBody)

		url := os.Getenv("FAST") + "/jobs"
		req, err := http.NewRequest(http.MethodPost, url, bodyReader)
		if err != nil {
			fmt.Printf("\n[ERR] HTTP POST: could not create request: %s\n", err)
			os.Exit(1)
		}
		req.Header.Set("Content-Type", "application/json")

		client := http.Client{
			Timeout: 30 * time.Second,
		}

		res, err := client.Do(req)
		if err != nil {
			fmt.Printf("\n[ERR] HTTP: error making http request: %s\n", err)
			os.Exit(1)
		}
		defer res.Body.Close()

		fmt.Printf("\n\n%+v", res)
		fmt.Printf("\n\n%+v", res.Body)

		//output, err := io.ReadAll(res.Body)
		// b, err := ioutil.ReadAll(resp.Body)  Go.1.15 and earlier
		//if err != nil {
		//	log.Fatalln(err)
		//}

		// Instead, prefer a context short-hand:
		//id := fmt.Sprintf("%d", user.TGID)
		//return c.Send("LANG: " + user.LanguageCode + " USER: " + id + " | " + user.Username + " TEXT: " + text)

		url = os.Getenv("FAST") + "/jobs/" + user.SessionID
		fmt.Printf("\n ===> %s", url)

		req, err = http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			fmt.Printf("\n[ERR] HTTP: could not create request: %s\n", err)
			os.Exit(1)
		}
		req.Header.Set("Content-Type", "application/json")

		var job Job
		for job.Status != "finished" {
			// TODO: Better and robust handling with error checking and deadlines

			//client := http.Client{
			//    Timeout: 30 * time.Second,
			//}

			// TODO: Error Handling
			res, err := client.Do(req)
			if err != nil {
				fmt.Printf("\n[ERR] HTTP GET: could not create request: %s\n", err)
				os.Exit(1)
			}

			output, err := io.ReadAll(res.Body)

			fmt.Printf("\n=> %+v", string(output))

			json.Unmarshal(output, &job)

			fmt.Printf("\n=> %+v", job)

			//body := "{\"id\": \"" + user.SessionID + "\", \"prompt\": \"" + prompt + "\"}"
			//bodyReader := bytes.NewReader([]byte(body))

		}

		return c.Send(string(job.Output))
	})

	b.Handle(tele.OnQuery, func(c tele.Context) error {
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
	b.Start()
}
