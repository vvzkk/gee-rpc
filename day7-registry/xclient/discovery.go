package xclient

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"
)

type SelectMode int

const (
	RandomSelect SelectMode = iota //select random  静态均衡算法的轮询法核随机法
	//iota，特殊常量，可以认为是一个可以被编译器修改的常量。
	RoundRobinSelect // select using Robbin algorithm
) //定义复数常量用括号
type Discovery interface {
	Refresh() error                      // refresh from remote registry从远程注册表刷新,从注册中心更新服务列表
	Update(servers []string) error       //手动更新服务列表
	Get(mode SelectMode) (string, error) // 根据负载均衡策略，选择一个服务实例
	GetAll() ([]string, error)           //返回所有的服务实例
}

//紧接着，我们实现一个不需要注册中心，服务列表由手工维护的服务发现的结构体：MultiServersDiscovery
// MultiServersDiscovery is a discovery for multi servers without a registry center
// user provides the server addresses explicitly instead  用户明确地提供服务地址
type MultiServersDiscovery struct {
	r       *rand.Rand   //generate random number生成随机数
	mu      sync.RWMutex //protect following
	servers []string     //服务序列
	index   int          //record the selected position for robin algorithm
	//记录robin算法的选定位置
}

// NewMultiServerDiscovery creates a MultiServersDiscovery instance
func NewMultiServerDiscovery(servers []string) *MultiServersDiscovery {
	d := &MultiServersDiscovery{
		servers: servers,
		r:       rand.New(rand.NewSource(time.Now().UnixNano())), //"时间戳（毫秒）
	} //r 是一个产生随机数的实例，初始化时使用时间戳设定随机数种子，避免每次产生相同的随机数序列。
	d.index = d.r.Intn(math.MaxInt32 - 1)
	//index 记录 Round Robin 算法已经轮询到的位置，为了避免每次从 0 开始，初始化时随机设定一个值。
	return d
}

//然后，实现 Discovery 接口
var _ Discovery = (*MultiServersDiscovery)(nil)

// Refresh doesn't make sense for MultiServersDiscovery, so ignore it
//刷新对 MultiServersDiscovery 没有意义，所以忽略它
func (d *MultiServersDiscovery) Refresh() error {
	return nil
}

// Update the servers of discovery dynamically if needed
//如果需要，动态更新发现服务器
func (d *MultiServersDiscovery) Update(servers []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers
	return nil
}
func (d *MultiServersDiscovery) Get(mode SelectMode) (string, error) {
	d.mu.Lock() //根据负载均衡策略，选择一个服务实例
	defer d.mu.Unlock()
	n := len(d.servers)
	if n == 0 {
		return "", errors.New("rpc discovery: no available servers")
	}
	switch mode {
	case RandomSelect:
		return d.servers[d.r.Intn(n)], nil
	case RoundRobinSelect:
		s := d.servers[d.index%n] // servers could be updated, so mode n to ensure safety
		d.index = (d.index + 1) % n
		return s, nil
	default:
		return "", errors.New("rpc discovery: not supported select mode")
	}
}

// returns all servers in discovery
func (d *MultiServersDiscovery) GetAll() ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	// return a copy of d.servers
	servers := make([]string, len(d.servers), len(d.servers))
	copy(servers, d.servers)
	return servers, nil
}
