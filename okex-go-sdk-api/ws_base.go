package okex

/*
 OKEX websocket api wrapper
 @author Lingting Fu
 @date 2018-12-27
 @version 1.0.0
*/

import (
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"

	rbt "github.com/emirpasic/gods/trees/redblacktree"
)

type BaseOp struct {
	Op   string   `json:"op"`
	Args []string `json:"args"`
}

func subscribeOp(sts []*SubscriptionTopic) (op *BaseOp, err error) {

	strArgs := []string{}

	for i := 0; i < len(sts); i++ {
		channel, err := sts[i].ToString()
		if err != nil {
			return nil, err
		}
		strArgs = append(strArgs, channel)
	}

	b := BaseOp{
		Op:   "subscribe",
		Args: strArgs,
	}
	return &b, nil
}

func unsubscribeOp(sts []*SubscriptionTopic) (op *BaseOp, err error) {

	strArgs := []string{}

	for i := 0; i < len(sts); i++ {
		channel, err := sts[i].ToString()
		if err != nil {
			return nil, err
		}
		strArgs = append(strArgs, channel)
	}

	b := BaseOp{
		Op:   CHNL_EVENT_UNSUBSCRIBE,
		Args: strArgs,
	}
	return &b, nil
}

func loginOp(apiKey string, passphrase string, timestamp string, sign string) (op *BaseOp, err error) {
	b := BaseOp{
		Op:   "login",
		Args: []string{apiKey, passphrase, timestamp, sign},
	}
	return &b, nil
}

type SubscriptionTopic struct {
	channel string
	filter  string `default:""`
}

func (st *SubscriptionTopic) ToString() (topic string, err error) {
	if len(st.channel) == 0 {
		return "", ERR_WS_SUBSCRIOTION_PARAMS
	}

	if len(st.filter) > 0 {
		return st.channel + ":" + st.filter, nil
	} else {
		return st.channel, nil
	}
}

type WSEventResponse struct {
	Event   string `json:"event"`
	Success string `json:success`
	Channel string `json:"channel"`
}

func (r *WSEventResponse) Valid() bool {
	return (len(r.Event) > 0 && len(r.Channel) > 0) || r.Event == "login"
}

type WSTableResponse struct {
	Table  string        `json:"table"`
	Action string        `json:"action",default:""`
	Data   []interface{} `json:"data"`
}

func (r *WSTableResponse) Valid() bool {
	return (len(r.Table) > 0 || len(r.Action) > 0) && len(r.Data) > 0
}

type WSDepthItem struct {
	InstrumentId string `json:"instrument_id"`
	Bids         *rbt.Tree
	Asks         *rbt.Tree
	Timestamp    string `json:"timestamp"`
	Checksum     int32  `json:"checksum"`
}

func first25Updates(t *rbt.Tree) [][4]interface{} {
	results := make([][4]interface{}, 0, 25)

	size := t.Size()
	iter := t.Iterator()

	if size > 25 {
		size = 25
	}

	for i := 0; i < size; i++ {
		iter.Next()
		value := iter.Value().([4]interface{})
		results = append(results, value)
	}

	return results
}

func newDepthItem(updates *WsDepthUpdates) *WSDepthItem {
	item := &WSDepthItem{
		InstrumentId: updates.InstrumentId,
		Checksum:     updates.Checksum,
		Timestamp:    updates.Timestamp,
		Bids:         rbt.NewWith(bidPriceLevelComparator),
		Asks:         rbt.NewWith(askPriceLevelComparator),
	}

	item.update(updates)
	return item
}

func mergeDepths(oldDepths *rbt.Tree, newDepths [][4]interface{}) error {
	for _, newItem := range newDepths {
		newPrice, err := strconv.ParseFloat(newItem[0].(string), 10)
		if err != nil {
			return fmt.Errorf("Bad price, check why. err: %+v", err)
		}

		newNum := StringToInt64(newItem[1].(string))

		if newNum == 0 {
			oldDepths.Remove(newPrice)
			continue
		}

		oldDepths.Put(newPrice, newItem)
	}

	return nil
}

func (di *WSDepthItem) update(newDI *WsDepthUpdates) error {
	err1 := mergeDepths(di.Asks, newDI.Asks)
	if err1 != nil {
		return err1
	}

	err2 := mergeDepths(di.Bids, newDI.Bids)
	if err2 != nil {
		return err2
	}

	askDepths := first25Updates(di.Asks)
	bidDepths := first25Updates(di.Bids)
	crc32BaseBuffer, expectCrc32 := calCrc32(&askDepths, &bidDepths)

	if expectCrc32 != newDI.Checksum {
		return fmt.Errorf("Checksum's not correct. LocalString: %s, LocalCrc32: %d, RemoteCrc32: %d",
			crc32BaseBuffer.String(), expectCrc32, newDI.Checksum)
	} else {
		di.Checksum = newDI.Checksum
		di.Timestamp = newDI.Timestamp
	}

	return nil
}

func calCrc32(askDepths *[][4]interface{}, bidDepths *[][4]interface{}) (bytes.Buffer, int32) {
	crc32BaseBuffer := bytes.Buffer{}
	crcAskDepth, crcBidDepth := 25, 25
	if len(*askDepths) < 25 {
		crcAskDepth = len(*askDepths)
	}
	if len(*bidDepths) < 25 {
		crcBidDepth = len(*bidDepths)
	}
	if crcAskDepth == crcBidDepth {
		for i := 0; i < crcAskDepth; i++ {
			if crc32BaseBuffer.Len() > 0 {
				crc32BaseBuffer.WriteString(":")
			}
			crc32BaseBuffer.WriteString(
				fmt.Sprintf("%v:%v:%v:%v",
					(*bidDepths)[i][0], (*bidDepths)[i][1],
					(*askDepths)[i][0], (*askDepths)[i][1]))
		}
	} else {
		for i := 0; i < crcBidDepth; i++ {
			if crc32BaseBuffer.Len() > 0 {
				crc32BaseBuffer.WriteString(":")
			}
			crc32BaseBuffer.WriteString(
				fmt.Sprintf("%v:%v", (*bidDepths)[i][0], (*bidDepths)[i][1]))
		}

		for i := 0; i < crcAskDepth; i++ {
			if crc32BaseBuffer.Len() > 0 {
				crc32BaseBuffer.WriteString(":")
			}
			crc32BaseBuffer.WriteString(
				fmt.Sprintf("%v:%v", (*askDepths)[i][0], (*askDepths)[i][1]))
		}
	}
	expectCrc32 := int32(crc32.ChecksumIEEE(crc32BaseBuffer.Bytes()))
	return crc32BaseBuffer, expectCrc32
}

type WSDepthTableResponse struct {
	Table  string           `json:"table"`
	Action string           `json:"action",default:""`
	Data   []WsDepthUpdates `json:"data"`
}

type WsDepthUpdates struct {
	InstrumentId string           `json:"instrument_id"`
	Asks         [][4]interface{} `json:"asks"`
	Bids         [][4]interface{} `json:"bids"`
	Timestamp    string           `json:"timestamp"`
	Checksum     int32            `json:"checksum"`
}

func (r *WSDepthTableResponse) Valid() bool {
	return (len(r.Table) > 0 || len(r.Action) > 0) && strings.Contains(r.Table, "depth") && len(r.Data) > 0
}

type WSHotDepths struct {
	Table    string
	DepthMap map[string]*WSDepthItem
}

func NewWSHotDepths(tb string) *WSHotDepths {
	hd := WSHotDepths{}
	hd.Table = tb
	hd.DepthMap = map[string]*WSDepthItem{}
	return &hd
}

func (d *WSHotDepths) loadWSDepthTableResponse(r *WSDepthTableResponse) error {
	if d.Table != r.Table {
		return fmt.Errorf("Loading WSDepthTableResponse failed becoz of "+
			"WSTableResponse(%s) not matched with WSHotDepths(%s)", r.Table, d.Table)
	}

	if !r.Valid() {
		return errors.New("WSDepthTableResponse's format error.")
	}

	switch r.Action {
	case "partial":
		d.Table = r.Table
		for i := 0; i < len(r.Data); i++ {
			crc32BaseBuffer, expectCrc32 := calCrc32(&r.Data[i].Asks, &r.Data[i].Bids)
			if expectCrc32 == r.Data[i].Checksum {
				d.DepthMap[r.Data[i].InstrumentId] = newDepthItem(&r.Data[i])
			} else {
				return fmt.Errorf("Checksum's not correct. LocalString: %s, LocalCrc32: %d, RemoteCrc32: %d",
					crc32BaseBuffer.String(), expectCrc32, r.Data[i].Checksum)
			}
		}

	case "update":
		for i := 0; i < len(r.Data); i++ {
			newDI := r.Data[i]
			oldDI := d.DepthMap[newDI.InstrumentId]
			if oldDI != nil {
				if err := oldDI.update(&newDI); err != nil {
					return err
				}
			} else {
				d.DepthMap[newDI.InstrumentId] = newDepthItem(&newDI)
			}
		}

	default:
		break
	}

	for i := 0; i < len(r.Data); i++ {
		depth := d.DepthMap[r.Data[i].InstrumentId]
		ask1 := depth.Asks.Left().Key.(float64)
		bid1 := depth.Bids.Left().Key.(float64)
		if bid1 >= ask1 {
			return fmt.Errorf("bid1 larger than bid1, bids: %v, asks: %v", depth.Bids.Values(), depth.Asks.Values())
		}
	}

	return nil
}

type WSErrorResponse struct {
	Event     string `json:"event"`
	Message   string `json:"message"`
	ErrorCode int    `json:"errorCode"`
}

func (r *WSErrorResponse) Valid() bool {
	return len(r.Event) > 0 && len(r.Message) > 0 && r.ErrorCode >= 30000
}

func loadResponse(rspMsg []byte) (interface{}, error) {

	//log.Printf("%s", rspMsg)

	evtR := WSEventResponse{}
	err := JsonBytes2Struct(rspMsg, &evtR)
	if err == nil && evtR.Valid() {
		return &evtR, nil
	}

	dtr := WSDepthTableResponse{}
	err = JsonBytes2Struct(rspMsg, &dtr)
	if err == nil && dtr.Valid() {
		return &dtr, nil
	}

	tr := WSTableResponse{}
	err = JsonBytes2Struct(rspMsg, &tr)
	if err == nil && tr.Valid() {
		return &tr, nil
	}

	er := WSErrorResponse{}
	err = JsonBytes2Struct(rspMsg, &er)
	if err == nil && er.Valid() {
		return &er, nil
	}

	if string(rspMsg) == "pong" {
		return string(rspMsg), nil
	}

	return nil, err

}

type ReceivedDataCallback func(interface{}) error

func defaultPrintData(obj interface{}) error {
	switch obj.(type) {
	case string:
		fmt.Println(obj)
	default:
		msg, err := Struct2JsonString(obj)
		if err != nil {
			fmt.Println(err.Error())
			return err
		}
		fmt.Println(msg)

	}
	return nil
}

func bidPriceLevelComparator(left, right interface{}) int {
	leftPriceLevel := left.(float64)
	rightPriceLevel := right.(float64)

	if leftPriceLevel < rightPriceLevel {
		return 1
	}

	if leftPriceLevel > rightPriceLevel {
		return -1
	}

	return 0
}

func askPriceLevelComparator(left, right interface{}) int {
	leftPriceLevel := left.(float64)
	rightPriceLevel := right.(float64)

	if leftPriceLevel < rightPriceLevel {
		return -1
	}

	if leftPriceLevel > rightPriceLevel {
		return 1
	}

	return 0
}
