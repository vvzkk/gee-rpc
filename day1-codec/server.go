package geerpc

import (
	"encoding/json"
	"fmt"
	"geerpc/codec"
	"io"
	"log"
	"net"
	"reflect"
	"sync"
)

const MagicNumber = 0x3bef5c //请求序列
//编解码方式的设定用option来承载
type Option struct {
	MagicNumber int        // MagicNumber marks this's a geerpc request
	CodecType   codec.Type // client may choose different Codec to encode body
}

//默认选择
var DefaultOption = &Option{
	MagicNumber: MagicNumber,
	CodecType:   codec.GobType,
}

// Server represents an RPC Server.
type Server struct{} //首先定义了结构体 Server，没有任何的成员字段。
//一般来说，涉及协议协商的这部分信息，需要设计固定的字节来传输的。但是为了实现上更简单，
//GeeRPC 客户端固定采用 JSON 编码 Option，后续的 header 和 body 的编码方式由 Option 中的 CodeType 指定，
//用json编码Option（请求序列和编码方式），而后续的header和body由Option中的CodeType指定
// NewServer returns a new Server.
func NewServer() *Server {
	return &Server{}
}

// DefaultServer is the default instance of *Server.
var DefaultServer = NewServer()

//DefaultServer 是一个默认的 Server 实例，主要为了用户使用方便。
//如果想启动服务，过程是非常简单的，传入 listener 即可，tcp 协议和 unix 协议都支持。
//lis, _ := net.Listen("tcp", ":9999")
//geerpc.Accept(lis)
// Accept accepts connections on the listener and serves requests
// for each incoming connection.
//数据传进来（tcp报文）格式为：Option|Header1|Body1|Header1|Body2
func (server *Server) Accept(lis net.Listener) { //// Accept等待并返回下一个连接到该接口的连接
	for {                                        //服务端for循环
		conn, err := lis.Accept() //接受请求（tcp）
		if err != nil {           //err=nil时就是没有错误的时候
			log.Println("rpc server: accept error:", err)
			return //将其输入到logger
		}
		go server.ServeConn(conn) //go协程处理连接
	} //实现了 Accept 方式，net.Listener 作为参数
}

// Accept accepts connections on the listener and serves requests
// for each incoming connection.
func Accept(lis net.Listener) { DefaultServer.Accept(lis) }

// ServeConn runs the server on a single connection.
// ServeConn blocks, serving the connection until the client hangs up.
func (server *Server) ServeConn(conn io.ReadWriteCloser) {
	defer func() { _ = conn.Close() }() //对conn读取器的关闭
	var opt Option
	if err := json.NewDecoder(conn).Decode(&opt); err != nil { //令err为对opt的json编码输入
		log.Println("rpc server: options error: ", err)
		return //尝试从数据头部解析出option，得到编码类型Codec，只实现了gob编码
	}
	if opt.MagicNumber != MagicNumber {
		log.Printf("rpc server: invalid magic number %x", opt.MagicNumber)
		return //发生请求序列错误，写入日志
	}
	f := codec.NewCodecFuncMap[opt.CodecType] //获取创建对应Codec对象的函数，Codec类封装了怎么从conn中read，write，解析header和body的方法
	if f == nil {
		log.Printf("rpc server: invalid codec type %s", opt.CodecType)
		return //解编码方法类型为空写入日志
	}
	server.serveCodec(f(conn))
}

//首先使用 json.NewDecoder 反序列化得到 Option 实例，检查 MagicNumber 和 CodeType 的值是否正确。
//然后根据 CodeType 得到对应的消息编解码器，接下来的处理交给 serverCodec。
// invalidRequest is a placeholder for response argv when error occurs
var invalidRequest = struct{}{}

//serveCodec 的过程非常简单。主要包含三个阶段
//
//读取请求 readRequest
//处理请求 handleRequest
//回复请求 sendResponse
func (server *Server) serveCodec(cc codec.Codec) {
	sending := new(sync.Mutex) // make sure to send a complete response
	//处理请求是并发的，但是回复请求的报文必须是逐个发送的，
	//并发容易导致多个回复报文交织在一起，客户端无法解析。在这里使用锁(sending)保证。
	wg := new(sync.WaitGroup) // wait until all request are handled
	//用来等所有handleRequest协程退出后，才退出serverCodec函数，关闭连接
	//等待线程计数器
	//WaitGroup用于等待一组线程的结束。父线程调用Add方法来设定应等待的线程的数量。
	//每个被等待的线程在结束时应调用Done方法。
	//同时，主线程里可以调用Wait方法阻塞至所有线程结束。***
	//等待直到所有的请求被处理，就是解锁前协程依然存在
	for { ////for循环处理Heades和Bodies
		req, err := server.readRequest(cc) //readRequest
		if err != nil {
			if req == nil {
				break // it's not possible to recover, so close the connection
			}
			req.h.Error = err.Error()
			server.sendResponse(cc, req.h, invalidRequest, sending)
			continue //req,err
		}
		wg.Add(1) //Goroutine+1
		//Add方法向内部计数加上delta，delta可以是负数；如果内部计数器变为0，Wait方法阻塞等待的所有线程都会释放，如果计数器小于0，方法panic。
		//注意Add加上正数的调用应在Wait之前，否则Wait可能只会等待很少的线程。一般来说本方法应在创建新的线程或者其他应等待的事件之前调用。
		go server.handleRequest(cc, req, sending, wg) //handleRequest 使用了协程并发执行请求。
	} //sending 互斥锁用来保证sendresponse时发送完整的请求防止不同handleRequest协程交错响应客服端
	//wg 用来等所有handleRequest协程退出后，才退出serverCodec函数，关闭连接
	wg.Wait()      //Wait方法阻塞直到WaitGroup计数器减为0。
	_ = cc.Close() //处理请求是并发的
}

//request stores all information of a call
type request struct {
	h            *codec.Header // header of request
	argv, replyv reflect.Value // argv and replyv of request请求的参数和返回值，类型为reflect.Value
	//Value类型的零值表示不持有某个值。零值的IsValid方法返回false,reflect.Value表示的是参数值的类型
	//Value为go值提供了反射接口。
}

func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) { //对header的解析
	var h codec.Header //读取请求的Header
	if err := cc.ReadHeader(&h); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read header error:", err)
		} //尽力而为，只有在 header 解析失败时，才终止循环。
		return nil, err
	}
	return &h, nil
}

//(读取请求是对请求进行两项判断：（1.Header能成功解析，2.请求序列和请求函数的类型正确）
//处理请求，返回请求序列和请求参数的类型用于读取请求的判断
//回复请求，加协程锁，一个个进行回复，写入cc.Write(h, body))
func (server *Server) readRequest(cc codec.Codec) (*request, error) { //读取请求
	h, err := server.readRequestHeader(cc) //对Header进行解析
	if err != nil {
		return nil, err
	}
	req := &request{h: h} //解析出的header和body信息放req结构体里面
	// TODO: now we don't know the type of request argv
	// day 1, just suppose it's string
	req.argv = reflect.New(reflect.TypeOf(""))
	if err = cc.ReadBody(req.argv.Interface()); err != nil { //解析body
		log.Println("rpc server: read argv err:", err)
	} //请求参数类型错误，不是所有go类型值的Value表示都能使用所有方法
	return req, nil
}

//type Header struct { //数据结构Header
//	ServiceMethod string //format "Service.Method"
//	//ServiceMethod是服务名和方法名通常与 Go 语言中的结构体和方法相映射。
//	Seq   uint64 //请求序号
//	Error string //错误信息
//}

//回复请求，回复请求的报文必须是逐个发送的，并发容易导致多个回复报文交织在一起，
//客户端无法解析。在这里使用锁(sending)保证。
func (server *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex) {
	sending.Lock() //协程锁
	defer sending.Unlock()
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error:", err)
	} //写入请求类型错误
}

//处理请求可以是并发的
func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup) {
	// TODO, should call registered rpc methods to get the right replyv
	// day 1, just print argv and send a hello message
	defer wg.Done() //Done方法减少WaitGroup计数器的值，应在线程的最后执行。
	log.Println(req.h, req.argv.Elem())
	req.replyv = reflect.ValueOf(fmt.Sprintf("geerpc resp %d", req.h.Seq)) //请求序列类型
	server.sendResponse(cc, req.h, req.replyv.Interface(), sending)        //回复请求
}
