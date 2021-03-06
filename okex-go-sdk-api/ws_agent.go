package okex

/*
 OKEX websocket API agent
 @author Lingting Fu
 @date 2018-12-27
 @version 1.0.0
*/

import (
	"bytes"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/klauspost/compress/flate"
	"github.com/pkg/errors"
)

type OKWSAgent struct {
	baseUrl string
	config  *Config
	conn    *websocket.Conn

	wsEvtCh  chan interface{}
	wsErrCh  chan interface{}
	wsTbCh   chan interface{}
	stopCh   chan interface{}
	signalCh chan os.Signal

	callback       ReceivedDataCallback
	activeChannels map[string]bool
	hotDepthsMap   map[string]*WSHotDepths

	processMut sync.Mutex
}

func (a *OKWSAgent) Start(config *Config) error {
	a.baseUrl = config.WSEndpoint + "ws/v3?compress=true"
	if config.IsPrint {
		log.Printf("Connecting to %s", a.baseUrl)
	}

	c, _, err := websocket.DefaultDialer.Dial(a.baseUrl, nil)

	if err != nil {
		log.Fatalf("dial:%+v", err)
		return err
	}

	a.conn = c
	a.config = config

	a.wsEvtCh = make(chan interface{})
	a.wsErrCh = make(chan interface{})
	a.wsTbCh = make(chan interface{})
	a.stopCh = make(chan interface{}, 16)
	a.signalCh = make(chan os.Signal)
	a.activeChannels = make(map[string]bool)
	a.hotDepthsMap = make(map[string]*WSHotDepths)
	a.callback = config.Callback

	signal.Notify(a.signalCh, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	go a.work()
	go a.receive()
	go a.finalize()

	return nil
}

func (a *OKWSAgent) Subscribe(channel, filter string) error {
	a.processMut.Lock()
	defer a.processMut.Unlock()

	st := SubscriptionTopic{channel, filter}
	bo, err := subscribeOp([]*SubscriptionTopic{&st})
	if err != nil {
		return err
	}

	msg, err := Struct2JsonString(bo)
	if a.config.IsPrint {
		log.Printf("Send Msg: %s", msg)
	}
	if err := a.conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
		return err
	}

	return nil
}

func (a *OKWSAgent) UnSubscribe(channel, filter string) error {
	a.processMut.Lock()
	defer a.processMut.Unlock()

	st := SubscriptionTopic{channel, filter}
	bo, err := unsubscribeOp([]*SubscriptionTopic{&st})
	if err != nil {
		return err
	}

	msg, err := Struct2JsonString(bo)
	if a.config.IsPrint {
		log.Printf("Send Msg: %s", msg)
	}
	if err := a.conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
		return err
	}

	a.activeChannels[channel] = false

	return nil
}

func (a *OKWSAgent) Login(apiKey, passphrase string) error {

	timestamp := EpochTime()

	preHash := PreHashString(timestamp, GET, "/users/self/verify", "")
	if sign, err := HmacSha256Base64Signer(preHash, a.config.SecretKey); err != nil {
		return err
	} else {
		op, err := loginOp(apiKey, passphrase, timestamp, sign)
		data, err := Struct2JsonString(op)
		log.Printf("Send Msg: %s", data)
		err = a.conn.WriteMessage(websocket.TextMessage, []byte(data))
		if err != nil {
			return err
		}
		time.Sleep(time.Millisecond * 100)
	}
	return nil
}

func (a *OKWSAgent) keepalive() error {
	return a.ping()
}

func (a *OKWSAgent) Stop() error {
	defer func() {
		if a := recover(); a != nil {
			log.Printf("Stop End. Recover msg: %+v", a)
		}
	}()

	select {
	case <-a.stopCh:
	default:
		close(a.stopCh)
	}

	return nil
}

func (a *OKWSAgent) finalize() error {
	defer func() {
		if a.config.IsPrint {
			log.Printf("Finalize End. Connection to WebSocket is closed.")
		}
	}()

	select {
	case <-a.stopCh:
		if a.conn != nil {
			close(a.wsTbCh)
			close(a.wsEvtCh)
			close(a.wsErrCh)
			return a.conn.Close()
		}
	}

	return nil
}

func (a *OKWSAgent) ping() error {
	msg := "ping"
	//log.Printf("Send Msg: %s", msg)
	if err := a.conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
		return errors.Wrap(err, "write ping message")
	}

	return nil
}

var readerPool = sync.Pool{
	New: func() interface{} {
		return flate.NewReader(bytes.NewReader([]byte{}))
	},
}

func (a *OKWSAgent) GzipDecode(in []byte) ([]byte, error) {
	reader := readerPool.Get().(io.ReadCloser)
	reader.(flate.Resetter).Reset(bytes.NewReader(in), nil)
	defer func() {
		readerPool.Put(reader)
		reader.Close()
	}()

	return ioutil.ReadAll(reader)
}

func (a *OKWSAgent) handleErrResponse(r interface{}) error {
	if r == nil {
		return nil
	}

	log.Printf("handleErrResponse %+v \n", r)
	return nil
}

func (a *OKWSAgent) handleEventResponse(r interface{}) error {
	if r == nil {
		return nil
	}

	er := r.(*WSEventResponse)
	a.activeChannels[er.Channel] = (er.Event == CHNL_EVENT_SUBSCRIBE)
	return nil
}

func (a *OKWSAgent) handleTableResponse(r interface{}) error {
	if a.callback != nil {
		if err := a.callback(r); err != nil {
			return err
		}
	}
	return nil
}

func (a *OKWSAgent) work() {
	defer func() {
		if a := recover(); a != nil {
			log.Printf("Work End. Recover msg: %+v", a)
			debug.PrintStack()
		}
	}()

	defer a.Stop()

	ticker := time.NewTicker(14 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := a.keepalive(); err != nil {
				DefaultDataCallBack(err)
			}
		case errR := <-a.wsErrCh:
			a.handleErrResponse(errR)
		case evtR := <-a.wsEvtCh:
			a.handleEventResponse(evtR)
		case tb := <-a.wsTbCh:
			a.handleTableResponse(tb)
		case <-a.signalCh:
			break
		case <-a.stopCh:
			return
		}
	}
}

func (a *OKWSAgent) receive() {
	defer func() {
		if a := recover(); a != nil {
			log.Printf("Receive End. Recover msg: %+v", a)
			debug.PrintStack()
		}
	}()

	for {
		messageType, message, err := a.conn.ReadMessage()
		if err != nil {
			DefaultDataCallBack(err)
			break
		}

		txtMsg := message
		switch messageType {
		case websocket.TextMessage:
		case websocket.BinaryMessage:
			txtMsg, err = a.GzipDecode(message)
			if err != nil {
				DefaultDataCallBack(err)
				break
			}

		}

		rsp, err := loadResponse(txtMsg)
		if rsp != nil {
			if a.config.IsPrint {
				log.Printf("LoadedRep: %+v, err: %+v", rsp, err)
			}
		} else {
			log.Printf("TextMsg: %s", txtMsg)
		}

		if err != nil {
			break
		}

		switch v := rsp.(type) {
		case *WSErrorResponse:
			if v != nil {
				a.wsErrCh <- rsp
			}

		case *WSEventResponse:
			if v != nil {
				a.wsEvtCh <- v
			}

		case *WSDepthTableResponse:
			var err error
			dtr := rsp.(*WSDepthTableResponse)
			hotDepths := a.hotDepthsMap[dtr.Table]
			if hotDepths == nil {
				hotDepths = NewWSHotDepths(dtr.Table)
				err = hotDepths.loadWSDepthTableResponse(dtr)
				if err == nil {
					a.hotDepthsMap[dtr.Table] = hotDepths
				}
			} else {
				err = hotDepths.loadWSDepthTableResponse(dtr)
			}

			if err == nil {
				a.wsTbCh <- dtr
			} else {
				log.Printf("Failed to loadWSDepthTableResponse, dtr: %+v, err: %+v", dtr, err)
			}

		case *WSTableResponse:
			tb := rsp.(*WSTableResponse)
			a.wsTbCh <- tb
		default:
			//log.Println(rsp)
		}
	}
}
