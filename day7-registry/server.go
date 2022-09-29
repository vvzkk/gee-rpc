// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package geerpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"geerpc/codec"
	"io"
	"log"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
)

const MagicNumber = 0x3bef5c

type Option struct {
	MagicNumber    int           // MagicNumber marks this's a geerpc request
	CodecType      codec.Type    // client may choose different Codec to encode body
	ConnectTimeout time.Duration // 0 means no limit
	HandleTimeout  time.Duration
}

var DefaultOption = &Option{
	MagicNumber:    MagicNumber,
	CodecType:      codec.GobType,
	ConnectTimeout: time.Second * 10,
}

// Server represents an RPC Server.
type Server struct {
	serviceMap sync.Map
}

// NewServer returns a new Server.
func NewServer() *Server {
	return &Server{}
}

// DefaultServer is the default instance of *Server.
var DefaultServer = NewServer()

// ServeConn runs the server on a single connection.
// ServeConn blocks, serving the connection until the client hangs up.
func (server *Server) ServeConn(conn io.ReadWriteCloser) {
	defer func() { _ = conn.Close() }()
	var opt Option
	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("rpc server: options error: ", err)
		return
	}
	if opt.MagicNumber != MagicNumber {
		log.Printf("rpc server: invalid magic number %x", opt.MagicNumber)
		return
	}
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		log.Printf("rpc server: invalid codec type %s", opt.CodecType)
		return
	}
	server.serveCodec(f(conn), &opt)
}

// invalidRequest is a placeholder for response argv when error occurs
var invalidRequest = struct{}{}

func (server *Server) serveCodec(cc codec.Codec, opt *Option) {
	sending := new(sync.Mutex) // make sure to send a complete response
	wg := new(sync.WaitGroup)  // wait until all request are handled
	for {
		req, err := server.readRequest(cc)
		if err != nil {
			if req == nil {
				break // it's not possible to recover, so close the connection
			}
			req.h.Error = err.Error()
			server.sendResponse(cc, req.h, invalidRequest, sending)
			continue
		}
		wg.Add(1)
		go server.handleRequest(cc, req, sending, wg, opt.HandleTimeout)
	}
	wg.Wait()
	_ = cc.Close()
}

// request stores all information of a call
type request struct {
	h            *codec.Header // header of request
	argv, replyv reflect.Value // argv and replyv of request
	mtype        *methodType
	svc          *service
}

func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	var h codec.Header
	if err := cc.ReadHeader(&h); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read header error:", err)
		}
		return nil, err
	}
	return &h, nil
}

func (server *Server) findService(serviceMethod string) (svc *service, mtype *methodType, err error) {
	dot := strings.LastIndex(serviceMethod, ".")
	if dot < 0 {
		err = errors.New("rpc server: service/method request ill-formed: " + serviceMethod)
		return
	}
	serviceName, methodName := serviceMethod[:dot], serviceMethod[dot+1:]
	svci, ok := server.serviceMap.Load(serviceName)
	if !ok {
		err = errors.New("rpc server: can't find service " + serviceName)
		return
	}
	svc = svci.(*service)
	mtype = svc.method[methodName]
	if mtype == nil {
		err = errors.New("rpc server: can't find method " + methodName)
	}
	return
}

func (server *Server) readRequest(cc codec.Codec) (*request, error) {
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}
	req := &request{h: h}
	req.svc, req.mtype, err = server.findService(h.ServiceMethod)
	if err != nil {
		return req, err
	}
	req.argv = req.mtype.newArgv()
	req.replyv = req.mtype.newReplyv()

	// make sure that argvi is a pointer, ReadBody need a pointer as parameter
	argvi := req.argv.Interface()
	if req.argv.Type().Kind() != reflect.Ptr {
		argvi = req.argv.Addr().Interface()
	}
	if err = cc.ReadBody(argvi); err != nil {
		log.Println("rpc server: read body err:", err)
		return req, err
	}
	return req, nil
}

func (server *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex) {
	sending.Lock()
	defer sending.Unlock()
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error:", err)
	}
}

func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
	defer wg.Done()
	called := make(chan struct{})
	sent := make(chan struct{})
	go func() {
		err := req.svc.call(req.mtype, req.argv, req.replyv)
		called <- struct{}{}
		if err != nil {
			req.h.Error = err.Error()
			server.sendResponse(cc, req.h, invalidRequest, sending)
			sent <- struct{}{}
			return
		}
		server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
		sent <- struct{}{}
	}()

	if timeout == 0 {
		<-called
		<-sent
		return
	}
	select {
	case <-time.After(timeout):
		req.h.Error = fmt.Sprintf("rpc server: request handle timeout: expect within %s", timeout)
		server.sendResponse(cc, req.h, invalidRequest, sending)
	case <-called:
		<-sent
	}
}

// Accept accepts connections on the listener and serves requests
// for each incoming connection.
func (server *Server) Accept(lis net.Listener) {
	for {
		conn, err := lis.Accept()
		if err != nil {
			log.Println("rpc server: accept error:", err)
			return
		}
		go server.ServeConn(conn)
	}
}

// Accept accepts connections on the listener and serves requests
// for each incoming connection.
func Accept(lis net.Listener) { DefaultServer.Accept(lis) }

// Register publishes in the server the set of methods of the
// receiver value that satisfy the following conditions:
//	- exported method of exported type
//	- two arguments, both of exported type
//	- the second argument is a pointer
//	- one return value, of type error
func (server *Server) Register(rcvr interface{}) error {
	s := newService(rcvr)
	if _, dup := server.serviceMap.LoadOrStore(s.name, s); dup {
		return errors.New("rpc: service already defined: " + s.name)
	}
	return nil
}

// Register publishes the receiver's methods in the DefaultServer.
func Register(rcvr interface{}) error { return DefaultServer.Register(rcvr) }

const (
	connected        = "200 Connected to Gee RPC"
	defaultRPCPath   = "/_geeprc_"
	defaultDebugPath = "/debug/geerpc"
)

// ServeHTTP implements an http.Handler that answers RPC requests.
func (server *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != "CONNECT" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, "405 must CONNECT\n")
		return
	}
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		log.Print("rpc hijacking ", req.RemoteAddr, ": ", err.Error())
		return
	}
	_, _ = io.WriteString(conn, "HTTP/1.0 "+connected+"\n\n")
	server.ServeConn(conn)
}

// HandleHTTP registers an HTTP handler for RPC messages on rpcPath,
// and a debugging handler on debugPath.
// It is still necessary to invoke http.Serve(), typically in a go statement.
func (server *Server) HandleHTTP() {
	http.Handle(defaultRPCPath, server)
	http.Handle(defaultDebugPath, debugHTTP{server})
	log.Println("rpc server debug path:", defaultDebugPath)
}

// HandleHTTP is a convenient approach for default server to register HTTP handlers
func HandleHTTP() {
	DefaultServer.HandleHTTP()
}

//// Copyright 2009 The Go Authors. All rights reserved.
//// Use of this source code is governed by a BSD-style
//// license that can be found in the LICENSE file.
//
//package geerpc
//
//import (
//	"encoding/json"
//	"errors"
//	"fmt"
//	"geerpc/codec"
//	"io"
//	"log"
//	"net"
//	"net/http"
//	"reflect"
//	"strings"
//	"sync"
//	"time"
//)
//
//const MagicNumber = 0x3bef5c
//
//type Option struct {
//	MagicNumber    int           // MagicNumber marks this's a geerpc request
//	CodecType      codec.Type    // client may choose different Codec to encode body
//	ConnectTimeout time.Duration // 0 means no limit
//	HandleTimeout  time.Duration
//}
//
//var DefaultOption = &Option{
//	MagicNumber:    MagicNumber,
//	CodecType:      codec.GobType,
//	ConnectTimeout: time.Second * 10,
//}
//
//// Server represents an RPC Server.
//type Server struct {
//	serviceMap sync.Map
//}
//
//// NewServer returns a new Server.
//func NewServer() *Server {
//	return &Server{}
//}
//
//// DefaultServer is the default instance of *Server.
//var DefaultServer = NewServer()
//
//// ServeConn runs the server on a single connection.
//// ServeConn blocks, serving the connection until the client hangs up.
//func (server *Server) ServeConn(conn io.ReadWriteCloser) {
//	defer func() { _ = conn.Close() }()
//	var opt Option
//	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
//		log.Println("rpc server: options error: ", err)
//		return
//	}
//	if opt.MagicNumber != MagicNumber {
//		log.Printf("rpc server: invalid magic number %x", opt.MagicNumber)
//		return
//	}
//	f := codec.NewCodecFuncMap[opt.CodecType]
//	if f == nil {
//		log.Printf("rpc server: invalid codec type %s", opt.CodecType)
//		return
//	}
//	server.serveCodec(f(conn), &opt)
//}
//
//// invalidRequest is a placeholder for response argv when error occurs
//var invalidRequest = struct{}{}
//
//func (server *Server) serveCodec(cc codec.Codec, opt *Option) {
//	sending := new(sync.Mutex) // make sure to send a complete response
//	wg := new(sync.WaitGroup)  // wait until all request are handled
//	for {
//		req, err := server.readRequest(cc)
//		if err != nil {
//			if req == nil {
//				break // it's not possible to recover, so close the connection
//			}
//			req.h.Error = err.Error()
//			server.sendResponse(cc, req.h, invalidRequest, sending)
//			continue
//		}
//		wg.Add(1)
//		go server.handleRequest(cc, req, sending, wg, opt.HandleTimeout)
//	}
//	wg.Wait()
//	_ = cc.Close()
//}
//
//// request stores all information of a call
//type request struct {
//	h            *codec.Header // header of request
//	argv, replyv reflect.Value // argv and replyv of request
//	mtype        *methodType
//	svc          *service
//}
//
//func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
//	var h codec.Header
//	if err := cc.ReadHeader(&h); err != nil {
//		if err != io.EOF && err != io.ErrUnexpectedEOF {
//			log.Println("rpc server: read header error:", err)
//		}
//		return nil, err
//	}
//	return &h, nil
//}
//
//func (server *Server) findService(serviceMethod string) (svc *service, mtype *methodType, err error) {
//	dot := strings.LastIndex(serviceMethod, ".")
//	if dot < 0 {
//		err = errors.New("rpc server: service/method request ill-formed: " + serviceMethod)
//		return
//	}
//	serviceName, methodName := serviceMethod[:dot], serviceMethod[dot+1:]
//	svci, ok := server.serviceMap.Load(serviceName)
//	if !ok {
//		err = errors.New("rpc server: can't find service " + serviceName)
//		return
//	}
//	svc = svci.(*service)
//	mtype = svc.method[methodName]
//	if mtype == nil {
//		err = errors.New("rpc server: can't find method " + methodName)
//	}
//	return
//}
//
//func (server *Server) readRequest(cc codec.Codec) (*request, error) {
//	h, err := server.readRequestHeader(cc)
//	if err != nil {
//		return nil, err
//	}
//	req := &request{h: h}
//	req.svc, req.mtype, err = server.findService(h.ServiceMethod)
//	if err != nil {
//		return req, err
//	}
//	req.argv = req.mtype.newArgv()
//	req.replyv = req.mtype.newReplyv()
//
//	// make sure that argvi is a pointer, ReadBody need a pointer as parameter
//	argvi := req.argv.Interface()
//	if req.argv.Type().Kind() != reflect.Ptr {
//		argvi = req.argv.Addr().Interface()
//	}
//	if err = cc.ReadBody(argvi); err != nil {
//		log.Println("rpc server: read body err:", err)
//		return req, err
//	}
//	return req, nil
//}
//
//func (server *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex) {
//	sending.Lock()
//	defer sending.Unlock()
//	if err := cc.Write(h, body); err != nil {
//		log.Println("rpc server: write response error:", err)
//	}
//}
//
//func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
//	defer wg.Done()
//	called := make(chan struct{})
//	sent := make(chan struct{})
//	go func() {
//		err := req.svc.call(req.mtype, req.argv, req.replyv)
//		called <- struct{}{}
//		if err != nil {
//			req.h.Error = err.Error()
//			server.sendResponse(cc, req.h, invalidRequest, sending)
//			sent <- struct{}{}
//			return
//		}
//		server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
//		sent <- struct{}{}
//	}()
//
//	if timeout == 0 {
//		<-called
//		<-sent
//		return
//	}
//	select {
//	case <-time.After(timeout):
//		req.h.Error = fmt.Sprintf("rpc server: request handle timeout: expect within %s", timeout)
//		server.sendResponse(cc, req.h, invalidRequest, sending)
//	case <-called:
//		<-sent
//	}
//}
//
//// Accept accepts connections on the listener and serves requests
//// for each incoming connection.
//func (server *Server) Accept(lis net.Listener) {
//	for {
//		conn, err := lis.Accept()
//		if err != nil {
//			log.Println("rpc server: accept error:", err)
//			return
//		}
//		go server.ServeConn(conn)
//	}
//}
//
//// Accept accepts connections on the listener and serves requests
//// for each incoming connection.
//func Accept(lis net.Listener) { DefaultServer.Accept(lis) }
//
//// Register publishes in the server the set of methods of the
//// receiver value that satisfy the following conditions:
////	- exported method of exported type
////	- two arguments, both of exported type
////	- the second argument is a pointer
////	- one return value, of type error
//func (server *Server) Register(rcvr interface{}) error {
//	s := newService(rcvr)
//	if _, dup := server.serviceMap.LoadOrStore(s.name, s); dup {
//		return errors.New("rpc: service already defined: " + s.name)
//	}
//	return nil
//}
//
//// Register publishes the receiver's methods in the DefaultServer.
//func Register(rcvr interface{}) error { return DefaultServer.Register(rcvr) }
//
//const (
//	connected        = "200 Connected to Gee RPC"
//	defaultRPCPath   = "/_geerpc_"
//	defaultDebugPath = "/debug/geerpc" //defaultDebugPath 是为后续 DEBUG 页面预留的地址。
//)
//
//// ServeHTTP implements an http.Handler that answers RPC requests.
////ServeHTTP 实现了一个响应 RPC 请求的 http.Handler
//func (server *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
//	//这是GO语言的标准库里面的rpc源码
//	//Go 语言通过 ResponseWriter 对象发送 HTTP 响应
//	if req.Method != "CONNECT" {
//		w.Header().Set("Content-Type", "text/plain; charset=utf-8") //http响应报文的http响应头设置：
//		//HTTP 响应的首部字段，比如内容类型/编码、缓存控制、Cookie 信息等
//		w.WriteHeader(http.StatusMethodNotAllowed)
//		//WriteHeader 这个方法名有点误导，其实它并不是用来设置响应头的，该方法支持传入一个
//		//整型数据用来表示响应状态码，如果不调用该方法的话，默认响应状态码是 200 OK。
//		_, _ = io.WriteString(w, "405 must CONNECT\n")
//		return
//	}
//	conn, _, err := w.(http.Hijacker).Hijack()
//	if err != nil {
//		log.Print("rpc hijacking ", req.RemoteAddr, ": ", err.Error())
//		return
//	}
//	_, _ = io.WriteString(conn, "HTTP/1.0 "+connected+"\n\n")
//	server.ServeConn(conn)
//}
//
////这是一段接管 HTTP 连接的代码，所谓的接管 HTTP 连接是指这里接管了 HTTP 的 TCP 连接，*****
////也就是说 Golang 的内置 HTTP 库和 HTTPServer 库*****
////将不会管理这个 TCP 连接的生命周期，这个生命周期已经划给 Hijacker 了。******
//// HandleHTTP registers an HTTP handler for RPC messages on rpcPath.
////HandleHTTP 为 rpcPath 上的 RPC 消息注册一个 HTTP 处理程序。
//// It is still necessary to invoke http.Serve(), typically in a go statement.
//func (server *Server) HandleHTTP() {
//	http.Handle(defaultRPCPath, server) //注册htp处理程序
//}
//
//// HandleHTTP is a convenient approach for default server to register HTTP handlers
//func HandleHTTP() {
//	DefaultServer.HandleHTTP() //用默认的方法处理http响应
//}
//
////Go 语言中处理 HTTP 请求是非常简单的一件事，Go 标准库中 http.Handle 的实现如下：
////package http
////// Handle registers the handler for the given pattern
////// in the DefaultServeMux.
////// The documentation for ServeMux explains how patterns are matched.
////func Handle(pattern string, handler Handler) { DefaultServeMux.Handle(pattern, handler) }
////第一个参数是支持通配的字符串 pattern，在这里，我们固定传入 /_geeprc_，第二个参数是 Handler 类型，Handler 是一个接口类型，定义如下：
////type Handler interface {
////	ServeHTTP(w ResponseWriter, r *Request)
////}
////也就是说，只需要实现接口 Handler 即可作为一个 HTTP Handler 处理 HTTP 请求。
////接口 Handler 只定义了一个方法 ServeHTTP，实现该方法即可。
////当不想使用内置服务器的HTTP协议实现时，请使用Hijack。
////一般在在创建连接阶段使用HTTP连接，后续自己完全处理connection。
////go中自带的rpc可以直接复用http server处理请求的那一套流程去创建连接，
////连接创建完毕后再使用Hijack方法拿到连接。
////客户端通过向服务端发送method为connect的请求创建连接，创建成功后即可开始rpc调用。
