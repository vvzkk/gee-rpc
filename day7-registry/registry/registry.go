package registry

import (
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

//GeeRegistry is a simple register center, provide following functions.
// add a server and receive heartbeat to keep it alive.
// returns all alive servers and delete dead servers sync simultaneously.
//返回所有活着的服务器并同时删除死服务器同步。
type GeeRegistry struct { //GeeRegistry结构体
	timeout time.Duration //超时时间设置
	mu      sync.Mutex
	servers map[string]*ServerItem
}
type ServerItem struct {
	Addr  string //服务端口+开始时间
	start time.Time
}

const (
	defaultPath    = "/_geerpc_/registry"
	defaultTimeout = time.Minute * 5 //默认超时时间限制 ：5min
)

//New create a registry instance with timeout setting
func New(timeout time.Duration) *GeeRegistry { //实例创建
	return &GeeRegistry{
		servers: make(map[string]*ServerItem), //创建服务实例映射
		timeout: timeout,
	}
}

var DefaultGeeRegister = New(defaultTimeout) //默认超时时间设置为 5 min
//为 GeeRegistry 实现添加服务实例和返回服务列表的方法。
//putServer:添加服务实例，如果服务已经存在，则更新strat
//aliveServers：返回可用的服务列表，如果存在超时的服务，则删除
func (r *GeeRegistry) putServer(addr string) { //输入要用指针的形式
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.servers[addr]
	if s == nil {
		r.servers[addr] = &ServerItem{Addr: addr, start: time.Now()} //赋值要用引用的形式
	} else {
		s.start = time.Now() //// if exists, update start time to keep alive
	} //else要接在}之后
}
func (r *GeeRegistry) aliveServers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var alive []string
	for addr, s := range r.servers {
		if r.timeout == 0 || s.start.Add(r.timeout).After(time.Now()) {
			alive = append(alive, addr)
		} else {
			delete(r.servers, addr)
		}
	}
	sort.Strings(alive) //以首字母为标准进行升序排序
	return alive
}

//为了实现上的简单，GeeRegistry 采用 HTTP 协议提供服务，且所有的有用信息都承载在 HTTP Header 中。
//Get：返回所有可用的服务列表，通过自定义字段X-Geerpc-Servers承载
//Post：添加服务实例或者发送心跳，通过自定义字段X-Geerpc-Servers承载
// Runs at /_geerpc_/registry
func (r *GeeRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) { //写成serversHTTP
	switch req.Method {
	case "GET": //返回所有可用的服务列表，通过自定义字段 X-Geerpc-Servers 承载。
		// keep it simple, server is in req.Header
		w.Header().Set("X-Geerpc-Servers", strings.Join(r.aliveServers(), ","))
	case "POST": //添加服务实例或发送心跳，通过自定义字段 X-Geerpc-Server 承载
		addr := req.Header.Get("X-Geerpc-Server")
		if addr == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		r.putServer(addr) //添加服务实例，如果服务已经存在，则更新 start。
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// HandleHTTP registers an HTTP handler for GeeRegistry messages on registryPath
//HandleHTTP 在 registryPath 上为 GeeRegistry 消息注册一个 HTTP 处理程序
func (r *GeeRegistry) HandleHTTP(registryPath string) {
	http.Handle(registryPath, r)
	log.Println("rpc registry path:", registryPath)
}

func HandleHTTP() {
	DefaultGeeRegister.HandleHTTP(defaultPath)
}

//另外，提供 Heartbeat 方法，便于服务启动时定时向注册中心发送心跳，
//默认周期比注册中心设置的过期时间少 1 min。
// Heartbeat send a heartbeat message every once in a while
// it's a helper function for a server to register or send heartbeat
//helper 辅助函数
func Heartbeat(registry, addr string, duration time.Duration) {
	if duration == 0 {
		// make sure there is enough time to send heart beat
		// before it's removed from registry
		duration = defaultTimeout - time.Duration(1)*time.Minute
	}
	var err error
	err = sendHeartbeat(registry, addr)
	go func() {
		t := time.NewTicker(duration)
		for err == nil {
			<-t.C
			err = sendHeartbeat(registry, addr)
		}
	}()
}
func sendHeartbeat(registry, addr string) error {
	log.Println(addr, "send heart beat to registry", registry)
	httpClient := &http.Client{}
	req, _ := http.NewRequest("POST", registry, nil)
	req.Header.Set("X-Geerpc-Server", addr)
	if _, err := httpClient.Do(req); err != nil {
		log.Println("rpc server：heart beaterr", err)
		return err
	}
	return nil
}
