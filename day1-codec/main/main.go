package main

import (
	"encoding/json"
	"fmt"
	"geerpc"
	"geerpc/codec"
	"log"
	"net"
	"time"
)

func startServer(addr chan string) { //开启服务
	// pick a free port 选择一个不被占用的端口
	//服务端创建
	l, err := net.Listen("tcp", ":0") //Listen函数创建的服务端：
	if err != nil {
		log.Fatal("network error:", err)
	} //port is occpupied
	log.Println("start rpc server on", l.Addr())
	addr <- l.Addr().String() //字符串格式的地址输入到信道addr
	geerpc.Accept(l)          //Type Listener里面的函数Accept() (c Conn, err error)
	// Accept等待并返回下一个连接到该接口的连接
}

func main() {
	addr := make(chan string) //信道创建
	go startServer(addr)

	// in fact, following code is like a simple geerpc client
	conn, _ := net.Dial("tcp", <-addr) //tcp,l.Addr().String()
	//Dial函数和服务端建立连接：
	defer func() { _ = conn.Close() }()

	time.Sleep(time.Second) //
	//func Sleep
	//func Sleep(d Duration)
	//Sleep阻塞当前go程至少d代表的时间段。d<=0时，Sleep会立刻返回。
	// send options
	_ = json.NewEncoder(conn).Encode(geerpc.DefaultOption)
	//用json对option（请求序列，编码方式）进行编码，并且输入，
	//后续的 header 和 body 的编码方式由 Option 中的 CodeType 指定，后续Encoder中的conn的编码格式
	//在 startServer 中使用了信道 addr，确保服务端端口监听成功，客户端再发起请求。
	//客户端首先发送 Option 进行协议交换，接下来发送消息头 h := &codec.Header{}，和消息体 geerpc req ${h.Seq}。
	//最后解析服务端的响应 reply，并打印出来。
	cc := codec.NewGobCodec(conn) //以Gob的编码方式对conn进行编码后的值
	// send request & receive response
	for i := 0; i < 5; i++ {
		h := &codec.Header{
			ServiceMethod: "Foo.Sum", //请求方法
			Seq:           uint64(i), //请求序号
		}
		_ = cc.Write(h, fmt.Sprintf("geerpc req %d", h.Seq))
		_ = cc.ReadHeader(h) //判断cc.ReadHeader是否实现了h，如果没有则编译错误（用于类型断言？）
		//占位符，表示该位置应该已经赋值给了一个值，但是我们不需要这个值，
		//所以给下划线赋值，意思是不要扔掉，这样编译器可以更好的优化, 任何类型的单个值都可以抛出下划线。
		//??属于那一种情况
		var reply string
		_ = cc.ReadBody(&reply) //读取返回值
		log.Println("reply:", reply)
	}
} //_=   忽略结果，用于编译判断是否执行

//Dial函数和服务端建立连接：
//conn, err := net.Dial("tcp", "google.com:80")
//if err != nil {
//// handle error
//}
//fmt.Fprintf(conn, "GET / HTTP/1.0\r\n\r\n")
//status, err := bufio.NewReader(conn).ReadString('\n')
//// ...

//Listen函数创建的服务端：
//ln, err := net.Listen("tcp", ":8080")
//if err != nil {
//// handle error
//}
//for {
//conn, err := ln.Accept()
//if err != nil {
//// handle error
//continue
//}
//go handleConnection(conn)
//}
//| Option{MagicNumber: xxx, CodecType: xxx} | Header{ServiceMethod ...} | Body interface{} |
//| <------      固定 JSON 编码      ------>  | <-------   编码方式由 CodeType 决定   ------->|
