package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ContextKeys int

// context values
const (
	Attempts ContextKeys = iota
	Retry
)

const MAX_RETRIES = 3

type Backend struct {
	URL          *url.URL
	Alive        bool
	mux          sync.RWMutex
	ReverseProxy *httputil.ReverseProxy
}

type ServerPool struct {
	backends []*Backend
	current  uint64
}

// method to get next index atomically (preventing issues with concurrency)
// could also lock and unlock the mux but this is better
func (s *ServerPool) NextIndex() int {
	return int(atomic.AddUint64(&s.current, uint64(1)) % uint64(len(s.backends)))
}

// returns next active backend to take a connection
func (s *ServerPool) GetNext() *Backend {
	next := s.NextIndex()
	end := next + len(s.backends)
	for i := next; i < end; i++ {
		index := i % len(s.backends)
		if s.backends[index].IsAlive() {
			if i != next {
				atomic.StoreUint64(&s.current, uint64(index))
			}
			return s.backends[index]
		}
	}
	return nil
}

func (s *ServerPool) AddBackend(b *Backend) {
	s.backends = append(s.backends, b)
}

// backend methods (must be serializable to avoid race conditions)
// to learn how mux works (https://medium.com/bootdotdev/golang-mutexes-what-is-rwmutex-for-5360ab082626)
func (b *Backend) SetAlive(alive bool) {
	b.mux.Lock()
	b.Alive = alive
	b.mux.Unlock()
}

func (b *Backend) IsAlive() bool {
	b.mux.RLock()
	alive := b.Alive
	b.mux.RUnlock()
	return alive
}

func LoadBalance(w http.ResponseWriter, r *http.Request) {
	attempts := GetAttemptsFromContext(r)
	if attempts > MAX_RETRIES {
		log.Printf("%s(%s) Max attempts reached, terminating\n", r.RemoteAddr, r.URL.Path)
		http.Error(w, "Server unavailable.", http.StatusServiceUnavailable)
		return
	}

	if nextServer := serverPool.GetNext(); nextServer != nil {
		log.Println("Routing to ", nextServer.URL)
		nextServer.ReverseProxy.ServeHTTP(w, r)
		return
	}

	http.Error(w, "Server unavailable.", http.StatusServiceUnavailable)
}

func GetRetryFromContext(r *http.Request) int {
	if retry, ok := r.Context().Value(Retry).(int); ok {
		return retry
	}

	return 0
}

func GetAttemptsFromContext(r *http.Request) int {
	if attempts, ok := r.Context().Value(Attempts).(int); ok {
		return attempts
	}

	return 0
}

func isBackendAlive(u *url.URL) bool {
	timeout := 2 * time.Second
	conn, err := net.DialTimeout("tcp", u.Host, timeout)
	if err != nil {
		log.Println("Backend unavailable: ", err)
		return false
	}

	_ = conn.Close()
	return true
}

func (s *ServerPool) MarkBackendStatus(u *url.URL, alive bool) {
	for _, b := range s.backends {
		if b.URL.String() == u.String() {
			b.SetAlive(alive)
			break
		}
	}
}

func (s *ServerPool) HealthCheck() {
	for _, b := range s.backends {
		status := "up"
		alive := isBackendAlive(b.URL)
		b.SetAlive(alive)
		if !alive {
			status = "down"
		}
		log.Printf("%s [%s]\n", b.URL, status)
	}
}

func HealthCheck() {
	t := time.NewTicker(time.Second * 20)
	for range t.C {
		log.Println("Starting health check...")
		serverPool.HealthCheck()
		log.Println("Finished health check.")
	}
}

var serverPool ServerPool

func initializeBackends(tokens []string) {
	for _, tok := range tokens {
		serverUrl, err := url.Parse(tok)
		if err != nil {
			log.Fatal(err)
		}

		// reverse proxy directs client request to respective backend server
		proxy := httputil.NewSingleHostReverseProxy(serverUrl)

		// proxy takes a callback error function
		// we can use this to retry a connection
		proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, e error) {
			log.Printf("[%s] %s\n", serverUrl.Host, e.Error())
			retries := GetRetryFromContext(request)
			if retries < MAX_RETRIES {
				time.Sleep(10 * time.Millisecond)
				ctx := context.WithValue(request.Context(), Retry, retries+1)
				proxy.ServeHTTP(writer, request.WithContext((ctx)))
				return
			}

			serverPool.MarkBackendStatus(serverUrl, false)

			attempts := GetAttemptsFromContext(request)
			log.Printf("%s(%s) Attempting retry %d\n", request.RemoteAddr, request.URL.Path, attempts)
			ctx := context.WithValue(request.Context(), Attempts, attempts+1)
			LoadBalance(writer, request.WithContext(ctx))
		}

		backend := Backend{
			URL:          serverUrl,
			Alive:        true,
			ReverseProxy: proxy,
		}
		serverPool.AddBackend(&backend)
		log.Printf("Configured backend: %s\n", serverUrl)
	}
}

func main() {
	var serverList string
	var port int
	var testMode bool

	// command line args
	flag.StringVar(&serverList, "backends", "", "Backends (use commas to separate)")
	flag.IntVar(&port, "port", 3000, "Port to serve")
	flag.BoolVar(&testMode, "test", false, "Use test servers")
	flag.Parse()

	if testMode {
		// Use test servers
		log.Println("Running in test mode with test servers")
		ready := make(chan bool)
		go StartServers(ready)
		<-ready // wait for signal to continue
		tokens := make([]string, len(Ports))
		for i, p := range Ports {
			tokens[i] = "http://localhost:" + strconv.Itoa(p)
		}
		initializeBackends(tokens)
	} else {
		if len(serverList) == 0 {
			log.Fatal("Must have some backends")
		}
		tokens := strings.Split(serverList, ",")
		initializeBackends(tokens)
	}

	server := http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: http.HandlerFunc(LoadBalance),
	}

	go HealthCheck()

	log.Printf("Load balancer at :%d\n", port)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
