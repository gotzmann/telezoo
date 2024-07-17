package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	tele "gopkg.in/telebot.v3"
)

const VERSION = "0.32.0"

// [ ] TODO: Verify .etc hosts agains regexp
// [ ] TODO: USER => Store creation date
// [ ] TODO: Do not save empty users and duplicates into users.db
// [*] FIXME: fastHTTP.Do... => json.Unmarshal... => ERROR = invalid character 'R' looking for beginning of value | BODY = Requested ID was not found!
// [*] FIXME: ^^^ fastHTTP.Do... => json.Unmarshal... => ERROR = invalid character 'R' looking for beginning of value
// [ ] FIXME: Adapt TG version of Markdown for different models
// [ ] FIXME: If the .env was changed and there no more the host, that was sticked to the user or session, dump the older host!
// [ ] TODO: Detect wrong hosts on start? [ ERR ] HTTP POST: could not create request: parse "http://209.137.198.8 :15415/jobs": invalid character " " in host name
// [ ] FIXME: Inspect on start - are there another instance still running?
// [ ] TODO: daemond
// [*] TODO: Save user IDs into disk storage, SQLite vs json.Marshal?
// [ ] TODO: Send an empty message (rotated icon???) even before trying to call GPU?
// [ ] TODO: Paste eye catching picture inside Hello Message
// [-] TODO: Find great 13B / 30B LLaMA model for CHAT mode
// [*] TODO: Proper logging
// [*] TODO: Start dialog with short instructions on how to use chat commands
// [*] TODO: Handle SIGINT
// [*] TODO: Do graceful shutdown releasing all dialogs
// [*] TODO: Proper deadlines and retries for HTTP calls
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
	ID      string `json:"id"`
	Prompt  string `json:"prompt"`
	Session string `json:"session"`
	Output  string `json:"output,omitempty"`
	Status  string `json:"status,omitempty"`
}

type User struct {
	ID        string `json:"id,omitempty"` // User ID within external system
	Username  string `json:"username,omitempty"`
	TGID      int64  `json:"tgid,omitempty"`    // User ID within Telegram
	Mode      string `json:"mode,omitempty"`    // pro / chat
	SessionID string `json:"session,omitempty"` // current session
	// NB! Do not serialize status into DB before server do not start right for users with "processing" tasks
	Status string `json:"status,omitempty"` // processing status
	Server string `json:"server,omitempty"` // Server address for sticky sessions
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
		"Могу поддержать разговор на любую тему, просто пиши в чат.\n\n" +
		//"Если потребуется что-то посерьезнее, переключи меня в режим PRO - ведь это бесплатно.\n\n" +
		"Рекомендую запомнить эти команды:\n\n" +
		"/new - начать новый диалог [ забыть историю ]\n"
	// "/chat - пообщаться о жизни [ отвечает быстро ]\n" +
	// "/pro - включить интеллект [ будет медленно ]\n"

	startMessage = "Старт новой сессии..." //+
	//	"Сразу поясню - я понимаю разные языки, в том числе русский и английский. " +
	//	"Могу поддержать разговор на любую тему, просто пиши в чат.\n\n" +
	//"Если потребуется что-то посерьезнее, переключи меня в режим PRO - ведь это бесплатно.\n\n" +
	//	"Рекомендую запомнить эти команды:\n\n" +
	//	"/new - начать новый диалог [ забыть прошлое ]\n"
	// "/chat - пообщаться о жизни [ отвечает быстро ]\n" +
	// "/pro - включить интеллект [ будет медленно ]\n"

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
		fmt.Printf("\n[ ERROR ] Cant load .env file, shutdown...\n\n")
		os.Exit(0)
	}

	// -- Start logging

	var zapWriter zapcore.WriteSyncer
	zapConfig := zap.NewProductionEncoderConfig()
	zapConfig.NameKey = "telezoo"
	zapConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	fileEncoder := zapcore.NewJSONEncoder(zapConfig)
	logFile, err := os.OpenFile("telezoo.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)

	// TODO: What if there two instances running in parallel?
	if err != nil {
		fmt.Printf("\n[ ERROR ] Can't init logging, shutdown...\n\n")
		os.Exit(0)
	}

	zapWriter = zapcore.AddSync(logFile)
	core := zapcore.NewTee(zapcore.NewCore(fileEncoder, zapWriter, zapcore.DebugLevel))
	logger := zap.New(core)
	log = logger.Sugar()

	fmt.Print("\n[ START ] TeleZoo v" + VERSION + " is starting...")
	log.Info("[ START ] TeleZoo v" + VERSION + " is starting...")

	// -- Init GPU pods

	chatZoo = strings.Split(os.Getenv("CHATZOO"), ",")
	proZoo = strings.Split(os.Getenv("PROZOO"), ",")
	zoo["chat"] = chatZoo
	zoo["pro"] = proZoo

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
				log.Info("[ERR] Can't dump users to DB file")
			} else {
				for _, user := range users {
					userJSON, _ := json.Marshal(*user)
					//fmt.Printf("\n\nUSER JSON: %s", string(userJSON)) // DEBUG
					db.WriteString(string(userJSON) + "\n")
				}
				db.Sync()
				db.Close()
			}
		}

		os.Exit(0)
	}()

	// --- Finish what needed in case of graceful shutdown or unexpected panic

	defer func() {
		signal.Stop(signalChan)

		reason := recover()
		if reason != nil {
			//Colorize("\n[light_magenta][ ERROR ][white] %s\n\n", reason)
			log.Errorw("[ ERR ] There's a panic", "msg", reason)
			//os.Exit(0)
		}

		log.Info("[ STOP ] TeleZoo was stopped. Chiao!")
		logger.Sync()
	}()

	// -- Load users from DB [ draft version using local file for faster development ]

	db, err := os.OpenFile("telezoo.db", os.O_RDONLY, 0644)
	scanner := bufio.NewScanner(db)

	for scanner.Scan() {
		userJSON := scanner.Text()
		user := &User{}
		err := json.Unmarshal([]byte(userJSON), &user)
		if err != nil || user.TGID == 0 {
			continue
		}
		// FIXME: Trying to reload status as is
		user.Status = "" // reset the status, but maybe lose some messages were been processing

		// Respawn dead servers
		if !isPodActive(user.Mode, user.Server) {
			user.Server = randomPod(user.Mode)
			user.SessionID = uuid.New().String()
			user.Status = ""
		}

		users[user.TGID] = user
	}

	db.Close()

	// -- Set up bot

	pref := tele.Settings{
		Token:     os.Getenv("TELEGRAM_TOKEN"),
		Poller:    &tele.LongPoller{Timeout: 10 * time.Second},
		ParseMode: "Markdown", // NB!
	}

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
		fmt.Printf("\n[ MSG ] New message: %s", prompt)

		// allow more time for important requests and less for those which might be ignored
		slowHTTP := http.Client{Timeout: 10 * time.Second}
		fastHTTP := http.Client{Timeout: 2 * time.Second}

		mu.Lock()
		user, found := users[tgUser.ID]
		mu.Unlock()

		// -- new user ?

		if !found {
			log.Infow("[ USER ] New user", "user", tgUser.ID)

			user = &User{
				ID:        "",
				TGID:      tgUser.ID,
				Username:  tgUser.Username,
				Mode:      "chat",
				Server:    randomPod("chat"),
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
				if user.SessionID == "" {
					user.SessionID = uuid.New().String()
				}
				allowProcessing = true
			}
			mu.Unlock()
			if allowProcessing {
				break
			}
			fmt.Printf(" [ WAIT-FOR-GPU-SLOT ] ") // DEBUG
			time.Sleep(300 * time.Millisecond)
		}

		id := uuid.New().String()

		job := Job{
			ID:      id,
			Prompt:  prompt,
			Session: user.SessionID,
		}

		// -- create JSON request body
		body, err := json.Marshal(job)
		if err != nil {
			user.Status = ""
			log.Errorw("[ ERR ] Problem marshalling request", "id", id, "prompt", prompt)
			return c.Send("Проблема с обработкой запроса, попробуйте убрать спецсимволы...")
		}
		//prompt = strings.Trim(string(safePrompt), `\"`)

		//body := fmt.Sprintf(`{"id":"%s","session":"%s","prompt":"%s"}`, id, user.SessionID, prompt)
		//bodyReader := bytes.NewReader([]byte(body))
		bodyReader := bytes.NewReader(body)

		// -- create HTTP request
		url := user.Server + "/jobs"
		req, err := http.NewRequest(http.MethodPost, url, bodyReader)
		if err != nil {
			user.Status = ""
			log.Errorw("[ ERR ] Could not create HTTP request", "id", id, "error", err.Error())
			return c.Send("Не могу работать с этим запросом :(")
		}
		req.Header.Set("Content-Type", "application/json")

		// -- send request to GPU pod
		res, err := slowHTTP.Do(req)
		if err != nil {
			user.Status = ""
			log.Errorw("[ ERR ] Problem with HTTP request", "id", id, "error", err.Error())
			return c.Send("Проблемы со связью, попробуйте еще раз...")
		}
		defer res.Body.Close()

		fmt.Printf("\n[ NET ] GPU POST Req was sent") // DEBUG

		if res.StatusCode != 200 {
			fmt.Printf("\n[ ERR ] Wrong status code = %d", res.StatusCode) // DEBUG
			log.Errorw("[ ERR ] Wrong status code while sending new job", "id", id, "code", res.StatusCode)
			fmt.Printf("[ ERR ] Wrong status code while sending new job : %d", res.StatusCode) // DEBUG

			// Requested ID was not found!
			// FIXME: Think again about right logic here
			//if res.StatusCode == 404 {
			//	user.Status = ""
			//	user.SessionID = "" // NB! Session will be created with a new request
			//	return c.Send("Неожиданная ошибка, попробуйте еще раз...")
			//}

			//time.Sleep(2000 * time.Millisecond) // wait in case of problems
			//continue
		}

		// wait for 3 sec to provide GPU with some time to start doing the task
		time.Sleep(3000 * time.Millisecond)

		url = user.Server + "/jobs/" + id
		req, err = http.NewRequest(http.MethodGet, url, nil)
		// There should not be an errors at all, so just log it and return nothing
		if err != nil {
			user.Status = ""
			log.Errorw("[ ERR ] Unexpected problem while creating HTTP request", "id", id, "error", err.Error())
			//return c.Send("Неожиданная проблема на сервере :(")
			return nil
		}
		req.Header.Set("Content-Type", "application/json")

		//var job Job
		var errorAttempts int
		var msg *tele.Message
		for {

			//fmt.Printf("\nfastHTTP.Do...")
			// FIXME: Better and robust handling with error checking and deadlines
			res, err := fastHTTP.Do(req)
			if err != nil {
				fmt.Printf("\nERROR = %s", err.Error()) // DEBUG
				log.Errorw("[ ERR ] Problem with HTTP request", "id", id, "error", err.Error())
				//return c.Send("Проблемы со связью, попробуйте еще раз...")
				errorAttempts++
				if errorAttempts > 10 {
					user.Status = ""
					return c.Send("Проблемы со связью, попробуйте еще раз...")
				}
				time.Sleep(3000 * time.Millisecond) // wait in case of problems
				continue
			}

			fmt.Printf("\n[ NET ] GPU GET Req was sent") // DEBUG

			if res.StatusCode != 200 {
				fmt.Printf("\n[ ERR ] Wrong status code = %d", res.StatusCode) // DEBUG
				log.Errorw("[ ERR ] Wrong status code", "id", id, "code", res.StatusCode)

				// Requested ID was not found!
				// FIXME: Think again about right logic here
				if res.StatusCode == 404 {
					user.Status = ""
					user.SessionID = "" // NB! Session will be created with a new request
					return c.Send("Неожиданная ошибка, попробуйте еще раз...")
				}

				time.Sleep(3000 * time.Millisecond) // wait in case of problems
				continue
			}

			//fmt.Printf("\njson.Unmarshal...")
			body, err := io.ReadAll(res.Body)
			err = json.Unmarshal(body, &job) // TODO: Error Handling
			if err != nil {
				fmt.Printf("\nERROR = %s", err.Error())
				fmt.Printf("\nBODY = %s", body) // DEBUG
				log.Errorw("[ ERR ] Problem unmarshalling JSON response", "id", id, "error", err.Error(), "body", body)
				//return c.Send("Проблемы со связью, попробуйте еще раз...")
				errorAttempts++
				if errorAttempts > 8 {
					user.Status = ""
					return c.Send("Проблемы со связью, попробуйте еще раз...")
				}
				time.Sleep(3000 * time.Millisecond) // wait in case of problems
				continue
			}

			// do some replacing to allow correct Telegram Markdown
			// output := job.Output
			//output = strings.ReplaceAll(output, "\n* ", "\n- ") // TODO: bullet? middle point?
			//output = strings.ReplaceAll(output, "**", "*")
			//output = strings.ReplaceAll(output, "__", "_")

			output := ""
			prev := ""
			for _, rune := range job.Output + " " {
				switch {
				case prev == "**" && rune != '*':
					output += "*"
					prev = ""
				case prev == "*" && rune != '*':
					output += string("\\*")
					prev = ""
				case rune == '*':
					prev += "*"
					continue
				case rune != '*' && len(prev) > 0:
					output += strings.ReplaceAll(prev, "*", "\\*")
					prev = ""
				case rune == '[':
					output += string("\\[")
					continue
					//case rune == ']':
					//	output += string("\\]")
					//	continue
				}
				output += string(rune)
			}
			output = strings.Trim(output, " ")
			fmt.Printf("\n\nOUTPUT = %s", output) // DEBUG

			// output = "Hello!" // DEBUG
			eosFound := false
			if strings.Contains(output, "<|im_end|>") || strings.Contains(output, "<|eot_id|>") {
				fmt.Printf("\n[ <|END|> ]") // DEBUG
				eosFound = true

				pos := strings.Index(output, "<|eot_id|>")
				fmt.Printf("\n\npos = %v", pos)
				//utf8 := []rune(output)
				//fmt.Printf("\n\nutf8 = %v", utf8)
				output = string(output[:pos])
				fmt.Printf("\n\noutput = %v", output)

				//output = strings.ReplaceAll(output, "<|im_end|>", "") // FIXME: Nous 8B
				//output = strings.ReplaceAll(output, "<|eot_id|>", "") // FIXME: Nous 8B
				//output, eosFound = strings.CutSuffix(output, "<|eot_id|>") // FIXME: Nous 8B
				//eosFlag = true
			} /*
				output, found := strings.CutSuffix(output, "<|im_end|>") // FIXME: Nous 8B
				if found {
					break
				}
				output, found = strings.CutSuffix(output, "<|eot_id|>") // FIXME: Nous 8B
				if found {
					break
				}*/
			fmt.Printf("\n\nOUTPUT = %s | %v", output, eosFound) // DEBUG

			// create the message if needed, or edit existing with the new content
			if msg == nil && output != "" {
				fmt.Printf("\n\nNEW MSG will send = %s", output) // DEBUG
				msg, err = bot.Send(tgUser, output)
				fmt.Printf("\n\nSEND OK for tgUser %+v", tgUser) // DEBUG
				if err != nil {
					fmt.Printf("\n[ ERR ] nil message ERROR = %s", err.Error())
					log.Errorw("[ ERR ] Problem sending message", "id", id, "error", err.Error())
					//return c.Send("Проблемы со связью, попробуйте еще раз...")
					errorAttempts++
					if errorAttempts > 10 {
						user.Status = ""
						return c.Send("Проблемы со связью, попробуйте еще раз...")
					}
					time.Sleep(3000 * time.Millisecond) // wait in case of problems
					//continue
					//if foundFlag { // DEBUG
					//	break
					//}
				}
				//fmt.Printf("\nnil message...")
			} else if msg != nil {
				// FIXME: Do not edit too often?
				// ERROR = telegram: retry after 122 (429)
				fmt.Printf("\nbot.Edit...")
				_, err := bot.Edit(msg, output)
				if err != nil {
					fmt.Printf("\nmsg edit ERROR = %s", err.Error())
					log.Errorw("[ ERR ] Problem editing message", "id", id, "error", err.Error())
					//return c.Send("Проблемы со связью, попробуйте еще раз...")
					errorAttempts++
					if errorAttempts > 10 {
						user.Status = ""
						return c.Send("Проблемы со связью, попробуйте еще раз...")
					}
					time.Sleep(3000 * time.Millisecond) // wait for 1 sec in case of problems
					//continue
				}
				//if msg2 != msg {
				//	fmt.Printf("\nERROR msg1 != msg2 [ %+v ] [ %+v ]", msg, msg2)
				//}
				if eosFound { // DEBUG
					break
				}
			}

			// FIXME: We need MORE conditions to leave the loop
			if job.Status == "finished" {
				fmt.Printf("\njob.Status == finished...")
				break
			}

			// FIXME: Do not edit too often?
			// ERROR = telegram: retry after 122 (429)
			// TODO: Correct sleep time depending on how often we request message editing to conform TG limits
			//fmt.Printf("\nSleep...")
			fmt.Printf(" [ WAIT-WHILE-REQ-PROCESSED ] ") // DEBUG
			time.Sleep(3000 * time.Millisecond)
		}

		// TODO: Log finished message with time elapsed
		//return c.Send(string(job.Output))
		//fmt.Printf("\n\nFINISHED")

		fmt.Printf("\nFinished...")
		log.Infow("[ MSG ] Message finished", "id", id)
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

	// -- Reset settings

	bot.Handle("/start", func(ctx tele.Context) error {
		return start(ctx)
	})

	// -- Start new session

	bot.Handle("/new", func(ctx tele.Context) error {
		return new(ctx)
	})
	bot.Handle("/туц", func(ctx tele.Context) error {
		return new(ctx)
	})
	bot.Handle("\\new", func(ctx tele.Context) error {
		return new(ctx)
	})

	// -- Switch into the PRO mode

	bot.Handle("/pro", func(ctx tele.Context) error {
		return pro(ctx)
	})

	// -- Switch into the CHAT mode

	bot.Handle("/chat", func(ctx tele.Context) error {
		return chat(ctx)
	})

	fmt.Printf("\n[ START ] Starting interchange with Telegram...")
	log.Info("[ START ] Start TG interchange...")
	bot.Start()
}

// -- start

func start(c tele.Context) error {
	tgUser := c.Sender()

	mu.Lock()
	user, found := users[tgUser.ID]
	mu.Unlock()

	if !found {
		return nil // FIXME: Is it possible?
	}

	user.Mode = "chat"
	user.Server = randomPod(user.Mode)
	user.SessionID = uuid.New().String()

	log.Infow("[ USER ] Start with /start command", "user", tgUser.ID)
	return c.Send(helloMessage)
}

// -- new

func new(c tele.Context) error {
	tgUser := c.Sender()

	mu.Lock()
	user, found := users[tgUser.ID]
	mu.Unlock()

	if !found {
		return nil // FIXME: Is it possible?
	}

	user.Server = randomPod(user.Mode)
	user.SessionID = uuid.New().String()

	log.Infow("[ USER ] New session", "user", tgUser.ID)
	return c.Send("Начинаю новую сессию...")
}

// -- pro

func pro(c tele.Context) error {
	tgUser := c.Sender()

	mu.Lock()
	user, found := users[tgUser.ID]
	mu.Unlock()

	if !found {
		return nil // FIXME: Is it possible?
	}

	user.Mode = "pro"
	user.Server = randomPod(user.Mode)
	user.SessionID = uuid.New().String()

	log.Infow("[ USER ] Switched to PRO plan", "user", tgUser.ID)
	return c.Send("Включаю полную мощность...")
}

// -- chat

func chat(c tele.Context) error {
	tgUser := c.Sender()

	mu.Lock()
	user, found := users[tgUser.ID]
	mu.Unlock()

	if !found {
		return nil // FIXME: Is it possible?
	}

	user.Mode = "chat"
	user.Server = randomPod(user.Mode)
	user.SessionID = uuid.New().String()

	log.Infow("[ USER ] Switched to CHAT mode", "user", tgUser.ID)
	return c.Send("Переключаюсь в режим чата...")
}

// -- Helpers

func randomPod(mode string) string {
	max := len(zoo[mode])
	pod := rand.Intn(max)
	for pod == max {
		pod = rand.Intn(max)
	}
	return zoo[mode][pod]
}

func isPodActive(mode, pod string) bool {
	// TODO: Allow to switch for default mode when user mode is not supported
	//for _, mode := range []string{"chat", "pro"} {
	for _, envPod := range zoo[mode] {
		if pod == envPod {
			return true
		}
	}
	//}
	return false
}
