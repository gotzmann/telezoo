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

const VERSION = "0.13.0"

// [ ] TODO: Detect wrong hosts on start? [ ERR ] HTTP POST: could not create request: parse "http://209.137.198.8 :15415/jobs": invalid character " " in host name
// [ ] FIXME: Inspect on start - are there another instance still running?
// [ ] TODO: daemond
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
	zoo["pro"] = proZoo

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

	// -- Read existing users from local DB [ ugly draft for faster development ]

	db, err := os.OpenFile("telezoo.db", os.O_RDONLY, 0644)
	scanner := bufio.NewScanner(db)

	for scanner.Scan() {
		userJSON := scanner.Text()
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
		tgUser := c.Sender()
		prompt := c.Text()

		log.Infow("[ MSG ] New message", "user", tgUser.ID, "prompt", prompt)

		// allow more time for important requests and less for those which might be ignored
		slowHTTP := http.Client{Timeout: 5 * time.Second}
		fastHTTP := http.Client{Timeout: 1 * time.Second}

		mu.Lock()
		user, found := users[tgUser.ID]
		mu.Unlock()

		// -- new user ?

		if !found {
			log.Infow("[ USER ] New user", "user", tgUser.ID)

			pod := rand.Intn(len(chatZoo))
			for pod == len(chatZoo) {
				pod = rand.Intn(len(chatZoo))
			}

			user = &User{
				ID:        "",
				TGID:      tgUser.ID,
				Mode:      "chat",
				Server:    zoo["chat"][pod],
				SessionID: uuid.New().String(),
				Status:    "",
			}

			mu.Lock()
			users[tgUser.ID] = user
			mu.Unlock()

			// send hello message with instructions
			bot.Send(tgUser, helloMessage) // TODO: Handle errors
		}

		// catch processing GPU slot for the current request
		// or wait if there previous one which is not freed
		// this allows to process multiple DDoS requests from the same users sequentially
		// TODO: wathcdog / deadline to break deadlocks

		allowProcessing := false
		for {
			mu.Lock()
			if user.Status != "processing" {
				user.Status = "processing"
				allowProcessing = true
			}
			mu.Unlock()
			if allowProcessing {
				break
			}
			//fmt.Printf(" [ WAIT-FOR-GPU-SLOT ] ") // DEBUG
			time.Sleep(200 * time.Millisecond)
		}

		// -- create JSON request body
		id := uuid.New().String()
		body := "{ \"id\": \"" + id + "\", \"session\": \"" + user.SessionID + "\", \"prompt\": \"" + prompt + "\" }"
		bodyReader := bytes.NewReader([]byte(body))

		// -- create HTTP request
		url := user.Server + "/jobs"
		req, err := http.NewRequest(http.MethodPost, url, bodyReader)
		if err != nil {
			user.Status = ""
			log.Errorf("[ ERR ] Could not create HTTP request", "msg", err)
			return c.Send("Не могу работать с этим запросом :(")
		}
		req.Header.Set("Content-Type", "application/json")

		// -- send request to GPU pod
		res, err := slowHTTP.Do(req)
		if err != nil {
			user.Status = ""
			log.Errorf("[ ERR ] Problem with HTTP request", "msg", err)
			return c.Send("Проблемы со связью, попробуйте еще раз...")
		}
		defer res.Body.Close()

		// wait for 1 sec to provide GPU with some time to start doing the task
		time.Sleep(1000 * time.Millisecond)

		url = user.Server + "/jobs/" + id
		req, err = http.NewRequest(http.MethodGet, url, nil)
		// There should not be an errors at all, so just log it and return nothing
		if err != nil {
			user.Status = ""
			log.Errorf("[ ERR ] Unexpected problem while creating HTTP request", "msg", err)
			//return c.Send("Неожиданная проблема на сервере :(")
			return nil
		}
		req.Header.Set("Content-Type", "application/json")

		var job Job
		var msg *tele.Message
		for {

			// FIXME: Better and robust handling with error checking and deadlines
			res, err := fastHTTP.Do(req)
			if err != nil {
				log.Errorf("[ERR] Problem with HTTP request", "msg", err)
				//return c.Send("Проблемы со связью, попробуйте еще раз...")
				time.Sleep(1000 * time.Millisecond) // wait for 1 sec in case of problems
				continue
			}

			output, err := io.ReadAll(res.Body)
			json.Unmarshal(output, &job) // TODO: Error Handling

			// create the message if needed, or edit existing with the new content
			if msg == nil {
				msg, _ = bot.Send(tgUser, job.Output)
			} else {
				bot.Edit(msg, job.Output)
			}

			// FIXME: We need MORE conditions to leave the loop
			if job.Status == "finished" {
				break
			}

			//fmt.Printf(" [ WAIT-WHILE-REQ-PROCESSED ] ") // DEBUG
			time.Sleep(300 * time.Millisecond)
		}

		//return c.Send(string(job.Output))
		//fmt.Printf("\n\nFINISHED")

		//mu.Lock()
		user.Status = "" // TODO: Enum all statuses and flow between them
		//mu.Unlock()

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

		pod := rand.Intn(len(chatZoo))
		for pod == len(chatZoo) {
			pod = rand.Intn(len(chatZoo))
		}

		mu.Lock()
		if user, ok := users[tgUser.ID]; ok {
			user.Mode = "chat"
			user.Server = zoo["chat"][pod]
			user.SessionID = uuid.New().String()
			//fmt.Printf("\n\nUSER FOUND !!!")
		}
		// FIXME: What if there no such user? After server restart, etc
		mu.Unlock()

		log.Infow("[ USER ] New session", "user", tgUser.ID)
		return c.Send("Начинаю новую сессию...")
	})

	// -- Switch into the PRO mode

	bot.Handle("/pro", func(c tele.Context) error {
		tgUser := c.Sender()

		pod := rand.Intn(len(proZoo))
		for pod == len(proZoo) {
			pod = rand.Intn(len(proZoo))
		}

		mu.Lock()
		if user, ok := users[tgUser.ID]; ok {
			user.Mode = "pro"
			user.Server = zoo["pro"][pod]
			user.SessionID = uuid.New().String()
			//fmt.Printf("\n\nUSER FOUND !!!")
		}
		// FIXME: What if there no such user? After server restart, etc
		mu.Unlock()

		log.Infow("[ USER ] Switched to PRO plan", "user", tgUser.ID)
		return c.Send("Включаю полную мощность...")
	})

	// -- Switch into the CHAT mode

	bot.Handle("/chat", func(c tele.Context) error {
		tgUser := c.Sender()

		pod := rand.Intn(len(chatZoo))
		for pod == len(chatZoo) {
			pod = rand.Intn(len(chatZoo))
		}

		mu.Lock()
		if user, ok := users[tgUser.ID]; ok {
			user.Mode = "chat"
			user.Server = zoo[user.Mode][pod]
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
