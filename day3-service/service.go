package geerpc

import (
	"go/ast"
	"log"
	"reflect"
	"sync/atomic" //提供对底层原子级的内存操作，对于同步算法的实现很有用
)

//但是服务端的功能并不完整，仅仅将请求的 header 打印了出来，****
//并没有真正地处理。那今天的主要目的是补全这部分功能。*****
//首先通过反射实现结构体与服务的映射关系,代码独立放置在 service.go 中******
//每一个 methodType 实例包含了一个方法的完整信息
//type Header struct { //数据结构Header
//	ServiceMethod string //format "Service.Method"
//	//ServiceMethod是服务名和方法名通常与 Go 语言中的结构体和方法相映射。
//	Seq   uint64 //请求序号
//	Error string //错误信息
//} //参数和返回值在body里面
type methodType struct {
	method    reflect.Method //方法本身
	ArgType   reflect.Type   //反射类型的变量reflect.Typeof（）
	ReplyType reflect.Type   //第二个参数类型reflect.Typeof()
	numCalls  uint64         //后续统计方法调用次数会用到
}

func (m *methodType) NumCalls() uint64 {
	return atomic.LoadUint64(&m.numCalls) //原子性地获取m.numCalls的值
}

//创建ArgvType类型的实例
func (m *methodType) newArgv() reflect.Value {
	var argv reflect.Value               //反射值的类型，用于作为返回值           //// arg may be a pointer type, or a value type
	if m.ArgType.Kind() == reflect.Ptr { //Kind代表Type类型值表示的具体分类。零值表示非法分类。
		//当参数类型为指针时
		argv = reflect.New(m.ArgType.Elem()) //获取该指针指向的元素类型的映射值
	} else {
		argv = reflect.New(m.ArgType).Elem() //ArgType类型的映射的Elem（）类型的值
	} ////get the first input param's addres获取第一个输入参数的地址
	return argv
}

//Elem()通过反射获取指针指向的元素类型,而reflect.New(Elem())是指针指向的元素类型的反射值
//比如 var x float64 = 3.4， v := reflect.ValueOf(x)
//fmt.Println("value:", v.Float())，value: 3.4
//t := reflect.TypeOf(target)
//if t.Kind() == reflect.Ptr { //指针类型获取真正type需要调用Elem
//t = t.Elem()
//}
//newStruc := reflect.New(t)// 调用反射创建对象

//创建Replyv类型的实例
func (m *methodType) newReplyv() reflect.Value {
	// reply must be a pointer type
	//get the new reply object pointer
	replyv := reflect.New(m.ReplyType.Elem()) //获取类型为ReloyType指针指向的元素类型的反射值
	switch m.ReplyType.Elem().Kind() {        //元素类型有两种
	case reflect.Map: //反射回来的值是Map
		replyv.Elem().Set(reflect.MakeMap(m.ReplyType.Elem())) //使replyv指向的元素的类型设为
		//使得replyv为Map值
	case reflect.Slice: //反射回来的值是切片
		replyv.Elem().Set(reflect.MakeSlice(m.ReplyType.Elem(), 0, 0))
	} //使得replyv为Slice
	return replyv
}

type service struct {
	name   string                 //映射的结构体的的名称
	typ    reflect.Type           //结构体的类型
	rcvr   reflect.Value          //结构体的实例本身，保留 rcvr 是因为在调用时需要 rcvr 作为第 0 个参数
	method map[string]*methodType //map类型，存储映射的结构体的所有符合条件的方法

}

//接下来，完成构造函数newService ，入参是任意需要映射为服务的结构体实例
func newService(rcvr interface{}) *service {
	s := new(service)
	s.rcvr = reflect.ValueOf(rcvr)
	s.name = reflect.Indirect(s.rcvr).Type().Name()
	s.typ = reflect.TypeOf(rcvr)
	if !ast.IsExported(s.name) {
		log.Fatalf("rpc server: %s is not a valid service name", s.name) //rpc服务本身一个有效名
	}
	s.registerMethods()
	return s

}
func (s *service) registerMethods() {
	//registerMethods过滤符合条件的方法；
	//两个到处或者内置类型的入参，（反射时为3个，第0个是自身，类似于python 的 self，java 中的 this）
	//返回值有且只有1个，类型为error
	//最后，我们还需要实现call方法，即能够调用反射值调用方法
	s.method = make(map[string]*methodType)
	for i := 0; i < s.typ.NumMethod(); i++ {
		method := s.typ.Method(i)
		mType := method.Type
		if mType.NumIn() != 3 || mType.NumOut() != 1 {
			continue
		}
		// NumIn()返回func类型的第i个参数的类型，如非函数或者i不在[0, NumIn())内将会panic
		// NumOut()返回func类型的第i个返回值的类型，如非函数或者i不在[0, NumOut())内将会panic
		if mType.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			continue
		}
		argType, replyType := mType.In(1), mType.In(2)
		if !isExportedOrBuiltinType(argType) || !isExportedOrBuiltinType(replyType) {
			continue
		}
		s.method[method.Name] = &methodType{
			method:    method,
			ArgType:   argType,
			ReplyType: replyType,
		}
		log.Printf("rpc server: register %s.%s\n", s.name, method.Name)
	}
}

func isExportedOrBuiltinType(t reflect.Type) bool {
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}
func (s *service) call(m *methodType, argv, replyv reflect.Value) error {
	atomic.AddUint64(&m.numCalls, 1)
	f := m.method.Func
	returnValues := f.Call([]reflect.Value{s.rcvr, argv, replyv})
	if errInter := returnValues[0].Interface(); errInter != nil {
		return errInter.(error)
	}
	return nil
}
