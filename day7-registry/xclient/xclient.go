package xclient

import (
	"context"
	. "geerpc"
	"io"
	"reflect"
	"sync"
)

//接下来，我们向用户暴露一个支持负载均衡的客户端 XClient。

type XClient struct {
	d       Discovery          //服务发现实例
	mode    SelectMode         //调度模式的选择，负载均衡模式
	opt     *Option            //协议选项
	mu      sync.Mutex         // protect following
	clients map[string]*Client //客服端请求实例序列
}

var _ io.Closer = (*XClient)(nil)

func NewXClient(d Discovery, mode SelectMode, opt *Option) *XClient {
	return &XClient{d: d, mode: mode, opt: opt, clients: make(map[string]*Client)}
}

func (xc *XClient) Close() error {
	xc.mu.Lock()
	defer xc.mu.Unlock()
	for key, client := range xc.clients {
		// I have no idea how to deal with error, just ignore it.
		_ = client.Close()
		delete(xc.clients, key)
	}
	return nil
} //为了尽量地复用已经创建好的 Socket 连接，使用 clients
// 保存创建成功的 Client 实例，并提供 Close 方法在结束后，关闭已经建立的连接
//接下来，实现客户端最基本的功能 Call。
func (xc *XClient) dial(rpcAddr string) (*Client, error) {
	xc.mu.Lock()
	defer xc.mu.Unlock()
	client, ok := xc.clients[rpcAddr] //(xc利用原有的clients连接)
	if ok && !client.IsAvailable() {  //检查 xc.clients 是否有缓存的 Client，如果有，检查是否是可用状态
		_ = client.Close() //如果是则返回缓存的 Client，如果不可用，则从缓存中删除
		delete(xc.clients, rpcAddr)
		client = nil
	}
	if client == nil {
		var err error
		client, err = XDial(rpcAddr, xc.opt) //如果步骤 1) 没有返回缓存的 Client
		if err != nil {                      //则说明需要创建新的 Client，缓存并返回。
			return nil, err
		}
		xc.clients[rpcAddr] = client
	}
	return client, nil
	//检查 xc.clients 是否有缓存的 Client，如果有，检查是否是可用状态，
	//如果是则返回缓存的 Client，如果不可用，则从缓存中删除。
	//如果步骤 1) 没有返回缓存的 Client
	//，则说明需要创建新的 Client，缓存并返回。
}
func (xc *XClient) call(rpcAddr string, ctx context.Context, serviceMethod string, args, reply interface{}) error {
	client, err := xc.dial(rpcAddr)
	if err != nil {
		return err
	}
	return client.Call(ctx, serviceMethod, args, reply)

}

//invokes 调用 call
// Call invokes the named function, waits for it to complete,
// and returns its error status.
// xc will choose a proper server.
func (xc *XClient) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	rpcAddr, err := xc.d.Get(xc.mode) //根据负载均衡策略，选择一个服务实例
	if err != nil {
		return err
	}
	return xc.call(rpcAddr, ctx, serviceMethod, args, reply)
}

//我们将复用 Client 的能力封装在方法 dial 中，dial 的处理逻辑如下：
//检查 xc.clients 是否有缓存的 Client，如果有，检查是否是可用状态，
//如果是则返回缓存的 Client，如果不可用，则从缓存中删除。
//如果步骤 1) 没有返回缓存的 Client
//，则说明需要创建新的 Client，缓存并返回。
//另外，我们为 XClient 添加一个常用功能：Broadcast。
func (xc *XClient) Broadcast(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	servers, err := xc.d.GetAll()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup //为了goroutine而设立的
	var mu sync.Mutex     //同步锁，为了数据访问而设立的,protect e and replyDone
	var e error
	replyDone := reply == nil // if reply is nil, don't need to set value
	ctx, cancel := context.WithCancel(ctx)
	for _, rpcAddr := range servers {
		wg.Add(1)
		go func(rpcAddr string) {
			defer wg.Done()
			var clonedReply interface{}
			if reply != nil {
				clonedReply = reflect.New(reflect.ValueOf(reply).Elem().Type()).Interface()
			}
			err := xc.call(rpcAddr, ctx, serviceMethod, args, clonedReply)
			mu.Lock()
			if err != nil && e == nil {
				e = err
				cancel() // if any call failed, cancel unfinished calls
			}
			if err != nil && !replyDone {
				reflect.ValueOf(reply).Elem().Set(reflect.ValueOf(clonedReply).Elem())
				replyDone = true
			}
			mu.Unlock()
		}(rpcAddr)
		// WithCancel()函数接受一个 Context 并返回其子Context和取消函数cancel
	}
	wg.Wait() //等待WaitGroup 归0子线程全部结束，父线程才结束
	return e
}
