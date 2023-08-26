package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os/signal"
	"syscall"

	"fmt"
	"io"

	//"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	tele "gopkg.in/telebot.v3"
)

const VERSION = "0.8.0"

// [*] TODO: Save user IDs into disk storage, SQLite vs json.Marshal?
// [ ] TODO: Send an empty message (rotated icon???) even before trying to call GPU?
// [ ] TODO: Catching picture into Hello Message
// [ ] TODO: Find great 13B LLaMA v2 based model for CHAT mode
// [*] TODO: Proper logging
// [*] TODO: Start dialog with short instructions on how to use chat commands
// [*] TODO: Handle SIGINT
// [*] TODO: Do graceful shutdown releasing all dialogs
// [ ] TODO: Proper deadlines and retries for HTTP calls
// [*] TODO: Do not os.Exit() or log.Fatal or panic!
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

	log *zap.SugaredLogger
)

type Job struct {
	ID     string `json: "id"`
	Prompt string `json: "prompt"`
	Output string `json: "output"`
	Status string `json: "status"`
}

type User struct {
	ID        string `json:"id,omitempty"`      // User ID within external system
	TGID      int64  `json:"tgid,omitempty"`    // User ID within Telegram
	Mode      string `json:"mode,omitempty"`    // pro / chat
	SessionID string `json:"session,omitempty"` // current session
	Status    string `json:"status,omitempty"`  // processing status
	Server    string `json:"server,omitempty"`  // Server address for sticky sessions
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

	helloMessage = "Привет! Я Мира. Похоже на первое знакомство :)\n\n" +
		"Сразу поясню - я понимаю разные языки, в том числе русский и английский. " +
		"Могу поддержать разговор на любую тему, просто напиши.\n\n" +
		"Если потребуется что-то посерьезнее, переключи меня в режим PRO - ведь это бесплатно.\n\n" +
		"Рекомендую запомнить эти команды:\n\n" +
		"/new - начать новый диалог [ забыть прошлое ]\n" +
		"/chat - пообщаться о жизни [ отвечает быстро ]\n" +
		"/pro - включить интеллект [ будет медленно ]\n"
)

func init() {
	users = make(map[int64]*User)
	sessions = make(map[string]string)
	zoo = make(map[string][]string)
}

func main() {

	// -- Read settings and init all

	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	// -- Start logging

	var zapWriter zapcore.WriteSyncer
	zapConfig := zap.NewProductionEncoderConfig()
	zapConfig.NameKey = "llamazoo" // TODO: pod name from config?
	//zapConfig.CallerKey = ""       // do not log caller like "llamazoo/llamazoo.go:156"
	zapConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	fileEncoder := zapcore.NewJSONEncoder(zapConfig)
	logFile, err := os.OpenFile( /*conf.Log*/ "telezoo.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	// TODO: What if there two instances running in parallel?
	if err != nil {
		fmt.Printf("\n[ ERROR ] Can't init logging, shutdown...\n\n")
		os.Exit(0)
	}
	zapWriter = zapcore.AddSync(logFile)
	core := zapcore.NewTee(zapcore.NewCore(fileEncoder, zapWriter, zapcore.DebugLevel))
	//logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	logger := zap.New(core)
	log = logger.Sugar()

	fmt.Print("\n[ START ] TeleZoo v" + VERSION + " is starting...")
	log.Info("[ START ] TeleZoo v" + VERSION + " is starting...")

	// -- Init GPU pods

	chatZoo = strings.Split(os.Getenv("CHATZOO"), ",")
	proZoo = strings.Split(os.Getenv("PROZOO"), ",")
	zoo["chat"] = chatZoo
	zoo["pro"] = chatZoo

	pref := tele.Settings{
		Token:  os.Getenv("TELEGRAM_TOKEN"),
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
	}

	// --- Allow graceful shutdown via OS signals
	// https://ieftimov.com/posts/four-steps-daemonize-your-golang-programs/

	signalChan := make(chan os.Signal)
	signal.Notify(signalChan,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)

	// --- Listen for OS signals in background

	go func() {

		select {
		case <-signalChan:

			// -- break execution immediate when DEBUG

			//if opts.Debug {
			//	Colorize("\n[light_magenta][ STOP ][light_blue] Immediate shutdown...\n\n")
			//	log.Info("[STOP] Immediate shutdown...")
			//	os.Exit(0)
			//}

			// -- wait while job will be done otherwise

			//server.GoShutdown = true
			//Colorize("\n[light_magenta][ STOP ][light_blue] Graceful shutdown...")
			fmt.Print("\n[ STOP ] Graceful shutdown...")
			log.Info("[ STOP ] Graceful shutdown...")
			//pending := len(server.Queue)
			//if pending > 0 {
			//	pending += 1 /*conf.Pods*/ // TODO: Allow N pods
			//	Colorize("\n[light_magenta][ STOP ][light_blue] Wait while [light_magenta][ %d ][light_blue] requests will be finished...", pending)
			//	log.Infof("[STOP] Wait while [ %d ] requests will be finished...", pending)
			//}

			// TODO: Backup an older file before rewrite?
			db, err := os.OpenFile("telezoo.db", os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				log.Info("[ERR] Cant dump users into file DB")
			} else {
				for _, user := range users {
					userJSON, _ := json.Marshal(*user)
					//fmt.Printf("\n\nUSER JSON: %s", string(userJSON)) // DEBUG
					db.WriteString(string(userJSON) + "\n")
				}
				db.Close()
			}
		}

		os.Exit(0)
	}()

	// --- Do all we need in case of graceful shutdown or unexpected panic

	defer func() {
		signal.Stop(signalChan)

		reason := recover()
		if reason != nil {
			//Colorize("\n[light_magenta][ ERROR ][white] %s\n\n", reason)
			log.Errorf("[ ERROR ] %s", reason)
			//os.Exit(0)
		}

		log.Info("[ STOP ] TeleZoo was stopped. Chiao!")
		logger.Sync()
	}()

	// -- Read registered users from local DB

	db, err := os.OpenFile("telezoo.db", os.O_RDONLY, 0644)
	scanner := bufio.NewScanner(db)
	//for _, user := range users {
	for scanner.Scan() {
		//userJSON, _ := json.Marshal(*user)
		userJSON := scanner.Text()
		//fmt.Printf("\n\nUSER JSON: %s", string(userJSON)) // DEBUG
		//db.WriteString(string(userJSON) + "\n")
		user := &User{}
		json.Unmarshal([]byte(userJSON), &user)
		user.Status = "" // reset the status, but loose last processing messages
		// TODO: Implement correct procedure to respawn dead servers
		users[user.TGID] = user
	}
	db.Close()

	// -- Set up bot

	bot, err := tele.NewBot(pref)
	if err != nil {
		log.Fatal("[ ERR ] Cant create TG bot instance")
		os.Exit(0)
	}

	// -- Handle user messages [ that weren't captured by other handlers ]

	bot.Handle(tele.OnText, func(c tele.Context) error {
		//log := logger.Sugar()

		tgUser := c.Sender()
		prompt := c.Text()

		//fmt.Printf("\n\nNEW REQ: %+v", prompt)
		log.Infow("[ MSG ] New message", "user", tgUser.ID, "prompt", prompt)

		var user *User
		var ok bool

		// -- new user ?

		mu.Lock()
		if user, ok = users[tgUser.ID]; !ok {
			//fmt.Printf("\n\nNEW USER: %d", tgUser.ID) // DEBUG
			log.Infow("[ USER ] New user", "user", tgUser.ID)
			user = &User{
				ID:        "",
				TGID:      tgUser.ID,
				Mode:      "chat",
				Server:    zoo["chat"][rand.Intn(len(chatZoo))],
				SessionID: uuid.New().String(),
				Status:    "",
			}
			users[tgUser.ID] = user
			// send hello message with instructions
			bot.Send(tgUser, helloMessage)
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

		//fmt.Printf("\n\nREQ: %s", body)

		url := /*os.Getenv("FAST")*/ user.Server + "/jobs"
		req, err := http.NewRequest(http.MethodPost, url, bodyReader)
		if err != nil {
			fmt.Printf("\n[ ERR ] HTTP POST: could not create request: %s\n", err)
			//os.Exit(1) // FIXME
			return c.Send("Проблемы со связью, попробуйте еще раз...")
		}
		req.Header.Set("Content-Type", "application/json")

		client := http.Client{
			Timeout: 5 * time.Second,
		}

		res, err := client.Do(req)
		if err != nil {
			//fmt.Printf("\n[ERR] HTTP: error making http request: %s\n", err)
			log.Errorf("[ ERR ] Problem with HTTP request", "msg", err)
			return c.Send("Проблемы со связью, попробуйте еще раз...")
		}
		defer res.Body.Close()

		//fmt.Printf("\n\n%+v", res)
		//fmt.Printf("\n\n%+v", res.Body)

		url = /*os.Getenv("FAST")*/ user.Server + "/jobs/" + id
		//fmt.Printf("\n ===> %s", url)

		req, err = http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			//fmt.Printf("\n[ERR] HTTP: could not create request: %s\n", err)
			log.Errorf("[ ERR ] Problem with HTTP request", "msg", err)
			return c.Send("Проблемы со связью, попробуйте еще раз...")
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
				//fmt.Printf("\n[ERR] HTTP GET: could not create request: %s\n", err)
				log.Errorf("[ERR] Problem with HTTP request", "msg", err)
				return c.Send("Проблемы со связью, попробуйте еще раз...")
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
		//fmt.Printf("\n\nFINISHED")

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
			//fmt.Printf("\n\nNEW SESSION") // DEBUG
			user.Server = zoo[user.Mode][rand.Intn(len(chatZoo))]
			user.SessionID = uuid.New().String()
		}
		// FIXME: What if there no such user? After server restart, etc
		mu.Unlock()

		log.Infow("[ USER ] New session", "user", tgUser.ID)
		return c.Send("Начинаю новую сессию...")
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

		log.Infow("[ USER ] Switched to PRO plan", "user", tgUser.ID)
		return c.Send("Включаю полную мощность...")
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

		log.Infow("[ USER ] Switched to CHAT mode", "user", tgUser.ID)
		return c.Send("Переключаюсь в режим чата...")
	})

	fmt.Printf("\n[ START ] Starting interchange with Telegram...")
	log.Info("[ START ] Start TG interchange...")
	bot.Start()
}
