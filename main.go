package main

import (
    "log"
    "os"
    "time"
    "fmt"
    "net/http"
    "bytes"

    tele "gopkg.in/telebot.v3"
    "github.com/joho/godotenv"
    resty "github.com/go-resty/resty/v2"
)

func main () {

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

    b.Handle("/hello", func(c tele.Context) error {
        return c.Send("Привет!")
    })

    b.Handle(tele.OnText, func(c tele.Context) error {
        // All the text messages that weren't
        // captured by existing handlers.

        var (
            user = c.Sender()
            text = c.Text()
        )
        // Use full-fledged bot's functions
        // only if you need a result:
        _, err := b.Send(user, text)
        if err != nil {
            return err
        }
        //msg = nil

        //res, err := http.Get(requestURL)
	      //if err != nil {
		    //    fmt.Printf("error making http request: %s\n", err)
		    //    os.Exit(1)
	      //}

        jsonBody := []byte(`{"id": "655ba2e7-ec5f-4a79-b131-f5e855463c88", "prompt": "Hello, Mira"}`)
        bodyReader := bytes.NewReader(jsonBody)

        requestURL := os.Getenv("FAST") + "/jobs"
        req, err := http.NewRequest(http.MethodPost, requestURL, bodyReader)
        if err != nil {
		        fmt.Printf("client: could not create request: %s\n", err)
		        os.Exit(1)
        }
        req.Header.Set("Content-Type", "application/json")

        client := http.Client{
            Timeout: 30 * time.Second,
        }

        res, err := client.Do(req)
        if err != nil {
	          fmt.Printf("client: error making http request: %s\n", err)
	          os.Exit(1)
        }

        fmt.Printf("%+v", res)

        // Instead, prefer a context short-hand:
        id := fmt.Sprintf("%d", user.ID)
        return c.Send("LANG: " + user.LanguageCode + " USER: " + id + " | "+ user.Username + " TEXT: " + text)
    })

    b.Handle(tele.OnQuery, func(c tele.Context) error {
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

    b.Start()
}
