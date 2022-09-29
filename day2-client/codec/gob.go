package codec

//Gob解编码方法
import (
	"bufio"
	"encoding/gob"
	"io"
	"log"
)

//先定义 GobCodec 结构体，这个结构体由四部分构成
type GobCodec struct {
	conn io.ReadWriteCloser //conn 是由构建函数传入，通常是通过 TCP 或者 Unix 建立 socket 时得到的链接实例
	buf  *bufio.Writer      //为了防止阻塞而创建的带缓冲的 Writer,一般这么做能提升性能。
	dec  *gob.Decoder       //解码器，反序列化     //  dec 和 enc 对应 gob 的 Decoder 和 Encoder
	enc  *gob.Encoder       //编码器，序列化
}

//type Codec interface { //Codec接口下的方法；1关闭数据流2.读取Header3读取body4写入Header和body
//	//，抽象出对消息体进行编解码的接口 Codec
//	//，抽象出接口是为了实现不同的 Codec 实例：
//	io.Closer                         //数据流的关闭
//	ReadHeader(*Header) error         //对结构体的读取
//	ReadBody(interface{}) error       //对body（参数和返回值的读取）
//	Write(*Header, interface{}) error //同时写入
//}
var _ Codec = (*GobCodec)(nil) //go的一个小技巧，用来确定Gobcodec实现了Codec的接口
//输入conn后先进行NewGobCodec的函数运行-然后返回值为Codec-运行Codec里面的方法
func NewGobCodec(conn io.ReadWriteCloser) Codec {
	buf := bufio.NewWriter(conn) //新建带缓冲的Writer内容为conn
	return &GobCodec{            //返回GobCodec型内容到Codec
		conn: conn, //io.Closer
		buf:  buf,
		dec:  gob.NewDecoder(conn), //新建Decoder对conn的解码信息操作的管理-从tcp传送到服务端后进行解码
		enc:  gob.NewEncoder(buf),  //新建Encoder对buf区的编码信息操作的管理-从客户端编码后传入tcp
	}
} //conn-slient-Encoder-tcp-Decoder-server
func (c *GobCodec) ReadHeader(h *Header) error { //当接受者为GobCodec类型，Codec接收口下的方法接受的参数形式
	return c.dec.Decode(h) //中间输入介入函数
	//Dncoder接口中包含了Dncode（）方法，对数据进行解码后传到远端给Header中
} //GobCodec类型的c 和Codec接口下ReadHeader方法的隐式实现，
//参数为c*GobCodec返回值为方法ReadHeader（h *Header）error
//使得读取的结构体h为c.dec.Decode(h)(就是GobCodec类型的解码得出的ServiceMethod，Seq，Error
//type Header struct { //数据结构Header
//	ServiceMethod string //format "Service.Method"
//	//ServiceMethod是服务名和方法名通常与 Go 语言中的结构体和方法相映射。
//	Seq   uint64 //请求序号
//	Error string //错误信息
//}
func (c *GobCodec) ReadBody(body interface{}) error {
	return c.dec.Decode(body) //GobCodec类型解码得出的body然后进行读取
}

//Encoder接口中包含了Encode（）方法，对数据进行编码后传到远端
func (c *GobCodec) Write(h *Header, body interface{}) (err error) {
	defer func() { //GobCodec类型的写入Header，body进行编码
		_ = c.buf.Flush()
		if err != nil {
			_ = c.Close()
		} //检测缓冲区并将数据流连接关闭操作
	}()
	if err := c.enc.Encode(h); err != nil { //enc是Encoder的简写，当err等于h的编码且不为空
		log.Println("rpc codec: gob error encoding header:", err)
		return err
	}
	if err := c.enc.Encode(body); err != nil { //对body进行编码
		log.Println("rpc codec: gob error encoding body:", err)
		return err
	} //Println调用l.Output将生成的格式化字符串输出到logger，参数用和fmt.Println相同的方法处理。
	return nil
}

func (c *GobCodec) Close() error {
	return c.conn.Close()
} //conn 的类型为io.ReadWriteCloser，关闭conn读取器
//func (*PipeReader) Close
//func (r *PipeReader) Close() error
//Close关闭读取器；关闭后如果对管道的写入端进行写入操作，就会返回(0, ErrClosedPip)。
