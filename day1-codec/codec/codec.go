package codec //消息编解码相关的代码都放到 codec 子目录
import "io"

//一个典型的 RPC 调用如下：
//
//err = client.Call("Arith.Multiply", args, &reply) 服务名.方法名，参数，返回值
//服务端的响应包括错误error和返回值reply两个
//Codec解编码器
type Header struct { //数据结构Header
	ServiceMethod string //format "Service.Method"
	//ServiceMethod是服务名和方法名通常与 Go 语言中的结构体和方法相映射。
	Seq   uint64 //请求序号
	Error string //错误信息
} //参数和返回值在body里面
//而header里面是请求序号，服务名和方法名
type Codec interface { //Codec接口下的方法；1关闭数据流2.读取Header3读取body4写入Header和body
	//，抽象出对消息体进行编解码的接口 Codec
	//，抽象出接口是为了实现不同的 Codec 实例：
	io.Closer                         //数据流的关闭
	ReadHeader(*Header) error         //对结构体的读取
	ReadBody(interface{}) error       //对body（参数和返回值的读取）
	Write(*Header, interface{}) error //同时写入
}
type NewCodecFunc func(io.ReadWriteCloser) Codec //type ...func()..是定义函数的意思
//创建新的编解码器函数NewCodecFunc，输入类型为io.ReadWriteCloser 返回值为Codec类型
type Type string

const (
	GobType  Type = "application/gob"  //定义字符串，字符串名 string=“xxxx”
	JsonType Type = "application/json" // not implemented
)

var NewCodecFuncMap map[Type]NewCodecFunc

//定义编解码函数映射 元素类型为NewCodecFunc-间接为func(io.ReadWriteCloser) Codec
func init() {
	NewCodecFuncMap = make(map[Type]NewCodecFunc) //映射创建
	NewCodecFuncMap[GobType] = NewGobCodec        //GobType类型编码对应的解编码器方法
}

//我们定义了 2 种 Codec，Gob 和 Json，但是实际代码中只实现了 Gob 一种，
//事实上，2 者的实现非常接近，甚至只需要把 gob 换成 json 即可。
//ServiceMethod是服务名和方法名通常与 Go 语言中的结构体和方法相映射。
//Seq 请求的序号，，也可以认为是某个请求的 ID，用来区分不同的请求
//Error 是错误信息，客户端置为空，服务端如果如果发生错误，将错误信息置于 Error 中。
//使用 encoding/gob 实现消息的编解码(序列化与反序列化)
//实现一个简易的服务端，仅接受消息，不处理，代码约 200 行
//一个RPC的典型调用：
//err=client.Call("Arith.Multiply",args,&reply)
//客户端发送的请求包括服务名Arith 方法名Multiply  参数args三个
//服务端的相应包括error 返回值reply两个
//我们将请求和响应中的参数和返回值抽象为 body，剩余的信息放在 header 中，
//那么就可以抽象出数据结构 Header：

//type Closer ¶
//type Closer interface {
//	Close() error
//}
//Closer is the interface that wraps the basic Close method.
//Closer 是封装了基本 Close 方法的接口。
//The behavior of Close after the first call is undefined.
//	Specific implementations may document their own behavior.
//	第一次调用后的关闭行为是未被定义的
//具体的实现可能记录他们的行为
//该接口比较简单，只有一个 Close() 方法，用于关闭数据流。
//文件 (os.File)、归档（压缩包）、数据库连接、Socket
//等需要手动关闭的资源都实现了 Closer 接口。
//实际编程中，经常将 Close 方法的调用放在 defer 语句中。
