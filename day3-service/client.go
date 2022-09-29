package geerpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"geerpc/codec"
	"io"
	"log"
	"net"
	"sync"
)

type Call struct { //封装结构体Call来承载一次 RPC 调用所需要的信息。
	Seq           uint64
	ServiceMethod string      //format"<servcie><Method>"
	Args          interface{} // arguments to the function
	Reply         interface{} //reply from the function
	Error         error       //f error occurs, it will be set
	Done          chan *Call  //Strobes when call is complete.//通话完成时闪烁。
}

func (call *Call) done() {
	call.Done <- call //
} //为了支持异步调用，Call 结构体中添加了一个字段 Done，Done
// 的类型是 chan *Call，当调用结束时，会调用 call.done() 通知调用方。也就向Done信道中输入

//client klaient 客服端用户端

// Client represents an RPC Client.
// There may be multiple outstanding Calls associated//可能有多个未完成的调用相关联
// with a single Client, and a Client may be used by
// multiple goroutines simultaneously.//单个客服端可能被多个goroutine同时使用
type Client struct {
	cc      codec.Codec //cc 是消息的编解码器，和服务端类似，用来序列化将要发送出去的请求，以及反序列化接收到的响应。
	opt     *Option
	sending sync.Mutex   // sending 是一个互斥锁，和服务端类似，为了保证请求的有序发送，即防止出现多个请求报文混淆。
	header  codec.Header //是每个请求的消息头，header 只有在请求发送时才需要，而
	// 请求发送是互斥的，因此每个客户端只需要一个，声明在 Client 结构体中可以复用。
	mu       sync.Mutex       //protect following
	seq      uint64           //seq 用于给发送的请求编号，每个请求拥有唯一编号。
	pending  map[uint64]*Call //存储未处理完的请求，键是编号，值是 Call 实例
	closing  bool             //user has called Close
	shutdown bool             //server has told us to stop
} //任意一个值置为 true，则表示 Client 处于不可用的状态，但有些许的差别，
//closing 是用户主动关闭的，即调用 Close 方法，而 shutdown 置为 true 一般是有错误发生
var _ io.Closer = (*Client)(nil) //go的一个小技巧，确定Client实现了io.Closer的接口
var ErrShutdown = errors.New("connection is shut down")

//errors包实现了创建错误值的函数。func New(text string) error
//使用字符串创建一个错误,请类比fmt包的Errorf方法，差不多可以认为是New(fmt.Sprintf(...))。
// Close the connection
func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing {
		return ErrShutdown
	}
	client.closing = true    //用户主动关闭，调用Close（）方法
	return client.cc.Close() //对Codec的流进行关闭
}
func (client *Client) IsAvailable() bool { //isavailable 表示可用
	client.mu.Lock()
	defer client.mu.Unlock()
	return !client.shutdown && !client.closing //可用的时候两个值都不为true
}

//紧接着，实现call相关的三个方法、客服端对call的处理与client实例相关（）
//registerCall：将参数 call 添加到 client.pending 中，并更新 client.seq。
func (client *Client) registerCall(call *Call) (uint64, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing || client.shutdown { //用户端不可用，中断，显示连接被关闭
		return 0, ErrShutdown
	}
	call.Seq = client.seq
	client.pending[call.Seq] = call //client的pending存储未处理完的请求，键是编号，值是 Call 实例
	client.seq++
	return call.Seq, nil //返回该请求的序列号

}

//removeCall：根据 seq，从 client.pending 中移除对应的 call，并返回。
func (client *Client) removeCall(seq uint64) *Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	call := client.pending[seq] //轮到该调用执行时，从客户端的未处理请求序列中移除，并返回该call
	delete(client.pending, seq)
	return call
}

//terminateCalls：服务端或客户端发生错误时调用，将 shutdown 设置为 true
//，且将错误信息通知所有 pending 状态的 call。
func (client *Client) terminateCalls(err error) {
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	client.shutdown = true
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

//seq序列
//pending待决的，待定的，待处理的；即将发生的，迫近的
//对一个客户端端来说，接收响应、发送请求是最重要的 2 个功能。那么首先实现接收功能，接收到的响应有三种情况：
//call 不存在，可能是请求没有发送完整，或者因为其他原因被取消，但是服务端仍旧处理了。
//call 存在，但服务端处理出错，即 h.Error 不为空。
//call 存在，服务端处理正常，那么需要从 body 中读取 Reply 的值。
func (client *Client) receive() { //接受响应，对不同类型的响应处理方法如上注释
	var err error
	for err == nil { //for循环对client的请求序列进行读取，err！=nil时结束
		var h codec.Header
		if err = client.cc.ReadHeader(&h); err != nil {
			break
		}
		call := client.removeCall(h.Seq)
		switch {
		case call == nil: //请求为空，令返回值为空
			// it usually means that Write partially failed
			// and call was already removed.
			err = client.cc.ReadBody(nil)
		case h.Error != "": //call存在，服务端出错h.Error不为空
			call.Error = fmt.Errorf(h.Error)
			err = client.cc.ReadBody(nil)
			call.done() //令返回值为空，当调用结束时，会调用 call.done() 通知调用方
		//也就向Done信道中输入
		default:
			err = client.cc.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading body" + err.Error())
			}
			call.done()
		}
	}
	// error occurs, so terminateCalls pending calls,
	client.terminateCalls(err) //服务端或客户端发生错误时调用(err!=nil)
}

//创建 Client 实例时，首先需要完成一开始的协议交换，即发送 Option 信息给服务端。
//协商好消息的编解码方式之后，再创建一个子协程调用 receive() 接收响应。
func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	f := codec.NewCodecFuncMap[opt.CodecType] //发送 Option 信息给服务端协商好解编码的方法后
	//再对信息进行传输
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", opt.CodecType)
		log.Println("rpc client: codec error:", err)
		return nil, err
	} // send options with server
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client: options error: ", err)
		_ = conn.Close()
		return nil, err
	} //以json的格式对opt进行编码，而conn的编码格式又由opt中的CodecType所决定
	return newClientCodec(f(conn), opt), nil
} //创建客服端的实例，发送Option信息信息给服务端协商好解编码的方法
func newClientCodec(cc codec.Codec, opt *Option) *Client {
	client := &Client{
		seq:     1,
		cc:      cc,
		opt:     opt,
		pending: make(map[uint64]*Call),
	} //创建客户端的解编码器同时接受服务端的响应
	go client.receive() //再创建一个子协程调用 receive() 接收响应，receive（）在上，设计对三种不同响应情况的处理
	return client
}

//还需要实现 Dial 函数，便于用户传入服务端地址，创建 Client 实例。
//为了简化用户调用，通过 ...*Option 将 Option 实现为可选参数。
func parseOptions(opts ...*Option) (*Option, error) {
	// if opts is nil or pass nil as parameter pəˈræmɪtər /参数
	//如果 opts 为 nil 或将 nil 作为参数传递
	if len(opts) == 0 || opts[0] == nil {
		return DefaultOption, nil
	} //编码方式为空，设为默认编码方式
	if len(opts) != 1 { //编码方式大于1，返回number of options is more than 1
		return nil, errors.New("number of options is more than 1")
	}
	opt := opts[0]
	opt.MagicNumber = DefaultOption.MagicNumber
	if opt.CodecType == "" { //为空选择默认编码方式
		opt.CodecType = DefaultOption.CodecType
	}
	return opt, nil
}

// Dial connects to an RPC server at the specified network address
//拨号连接到指定网络地址的RPC服务器
func Dial(network, address string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOptions(opts...) //可选传参方法
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}
	// close the connection if client is nil
	defer func() {
		if client == nil {
			_ = conn.Close()
		}
	}()
	return NewClient(conn, opt)
}

//服务端和客服端通过socket来进行数据交换，协议为tcp
//Dial函数和服务端建立连接：
//
//conn, err := net.Dial("tcp", "google.com:80")
//if err != nil {
//// handle error
//}
//fmt.Fprintf(conn, "GET / HTTP/1.0\r\n\r\n")
//status, err := bufio.NewReader(conn).ReadString('\n')
//// ...
//GeeRPC 客户端已经具备了完整的创建连接和接收响应的能力了，最后还需要实现发送请求的能力。
func (client *Client) send(call *Call) {
	// make sure that the client will send a complete request
	client.sending.Lock()
	defer client.sending.Unlock()

	// register this call.
	seq, err := client.registerCall(call) //将请求注册到client的请求序列里面
	if err != nil {
		call.Error = err
		call.done() //放生的错误则通知该call
		return
	}

	// prepare request header
	client.header.ServiceMethod = call.ServiceMethod //服务名和方法名的传递
	client.header.Seq = seq                          //请求序列的传递
	client.header.Error = ""                         //初始化错误信息为空

	// encode and send the request
	if err := client.cc.Write(&client.header, call.Args); err != nil {
		call := client.removeCall(seq) //client 的header和args（参数）写入成功，将该call从client的请求序列中移除
		// call may be nil, it usually means that Write partially failed,
		// client has received the response and handled
		if call != nil {
			call.Error = err
			call.done()
		}
	}
} //Wirte写入到数据流中 Read从数据流从读取，socket是socket streams类型采用tcp协议

// Go invokes the function asynchronously.
// It returns the Call structure representing the invocation.
func (client *Client) Go(serviceMethod string, args, reply interface{}, done chan *Call) *Call {
	if done == nil { //client的缓冲区检测
		done = make(chan *Call, 10)
	} else if cap(done) == 0 {
		log.Panic("rpc client: done channel is unbuffered")
	} //RPC客户端:已完成的通道没有缓冲
	call := &Call{ //请求的创建和赋值
		ServiceMethod: serviceMethod,
		Args:          args,
		Reply:         reply,
		Done:          done,
	}
	client.send(call) //检测缓冲区后进行请求的发送
	return call
}

// Call invokes the named function, waits for it to complete,
// and returns its error status.
func (client *Client) Call(serviceMethod string, args, reply interface{}) error {
	call := <-client.Go(serviceMethod, args, reply, make(chan *Call, 1)).Done //返回信息从信道done（Done）又重新输入回call
	return call.Error                                                         //返回call的error值（可以用于判断）
}

//Go 和 Call 是客户端暴露给用户的两个 RPC 服务调用接口，Go 是一个异步接口，返回 call 实例。
//Call 是对 Go 的封装，阻塞 call.Done，等待响应返回，是一个同步接口。
//至此，一个支持异步和并发的 GeeRPC 客户端已经完成。
