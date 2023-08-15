package main

import (
    "log"
    "os"
    "time"
    "fmt"

    tele "gopkg.in/telebot.v3"
    "github.com/joho/godotenv"
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
