package jsonrpcf

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/rpc"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

type Svc struct{}

func (*Svc) Sum(vals [2]int, res *int) error {
	*res = vals[0] + vals[1]
	return nil
}

func (*Svc) SumAll(vals []int, res *int) error {
	for _, v := range vals {
		*res += v
	}
	return nil
}

type NameArg struct{ Fname, Lname string }
type NameRes struct{ Name string }

func (*Svc) MapLen(m map[string]int, res *int) error {
	*res = len(m)
	return nil
}

func (*Svc) Name(t NameArg, res *NameRes) error {
	*res = NameRes{t.Fname + " " + t.Lname}
	return nil
}

func (*Svc) Err(struct{}, *struct{}) error {
	return errors.New("some issue")
}

func (*Svc) Err2(struct{}, *struct{}) error {
	return NewError(42, "some issue")
}

func (*Svc) Err3(struct{}, *struct{}) error {
	return &Error{42, "some issue", map[string]int{"one": 1, "two": 2}}
}

var svcMsg = make(chan string, 64)

func (*Svc) Msg(param [1]string, reply *struct{}) error {
	svcMsg <- param[0]
	return nil
}

func init() {
	_ = rpc.Register(&Svc{})
}

// Helpers

// caller return string which will overwrite testing.Log's caller with real one.
func caller() string {
	for skip := 1; ; skip++ {
		pc, _, _, ok := runtime.Caller(skip)
		if !ok {
			break
		}
		if strings.Index(runtime.FuncForPC(pc).Name(), ".Test") != -1 {
			return fmt.Sprintf("\r\t%*s\r\t%s: ", len(callerPos(1)), "", callerPos(skip))
		}
	}
	return ""
}

// callerPos return caller's info formatted in way used by testing.Log.
func callerPos(skip int) string {
	_, file, line, ok := runtime.Caller(1 + skip)
	if ok {
		file = filepath.Base(file)
	} else {
		file, line = "???", 1
	}
	return fmt.Sprintf("%s:%d", file, line)
}

func dump(got, want interface{}) string {
	if fmt.Sprintf("%T", got) != fmt.Sprintf("%T", want) {
		return fmt.Sprintf("exp: (%T) %#[1]v\ngot: (%T) %#[2]v\n", want, got)
	}
	return fmt.Sprintf("exp: %#v\ngot: %#v\n", want, got)
}

// testClient check output generated by client.Call().
func testClient(t *testing.T, cli, srv net.Conn, client *Client, method string, in interface{}, want string, wanterr *Error) {
	type Res struct {
		got string
		err error
	}
	read := make(chan Res, 1)
	done := make(chan *rpc.Call, 1)
	go func() {
		buf := bufio.NewReader(srv)
		got, err := buf.ReadString('\n')
		read <- Res{got, err}
	}()
	go client.Go(method, in, &struct{}{}, done)

	var err error
	var got string
	select {
	case call := <-done:
		err = call.Error
		cli.Write([]byte("\n"))
	case res := <-read:
		if res.err != nil {
			t.Fatalf("%ssrv.Read(), err = %v", caller(), res.err)
		}
		got = strings.TrimRight(res.got, "\n")
	}

	if err == nil && wanterr != nil || err != nil && (wanterr == nil || !reflect.DeepEqual(ServerError(err), wanterr)) {
		t.Errorf("%serr = %v, wanterr = %v", caller(), err, wanterr)
	}
	if got == want {
		return
	}
	var jgot, jwant interface{}
	if err := json.Unmarshal([]byte(got), &jgot); err != nil {
		t.Errorf("%s%s(%v), output err = %v\ngot: %#q", caller(), method, in, err, got)
	}
	if err := json.Unmarshal([]byte(want), &jwant); err != nil {
		t.Errorf("%s%s(%v), expect err = %v\nexp: %#q", caller(), method, in, err, want)
	}
	if !reflect.DeepEqual(jgot, jwant) {
		t.Errorf("%s%s(%v)\nexp: %#q\ngot: %#q", caller(), method, in, want, got)
	}
}

// testClient check output generated by client.Notify().
func testClientNotify(t *testing.T, cli, srv net.Conn, client *Client, method string, in interface{}, want string, wanterr *Error) {
	type Res struct {
		got string
		err error
	}
	read := make(chan Res, 1)
	done := make(chan error, 1)
	go func() {
		buf := bufio.NewReader(srv)
		got, err := buf.ReadString('\n')
		read <- Res{got, err}
	}()
	go func() {
		err := client.Notify(method, in)
		if err != nil {
			done <- err
		}
	}()

	var err error
	var got string
	select {
	case err = <-done:
		cli.Write([]byte("\n"))
	case res := <-read:
		if res.err != nil {
			t.Fatalf("%ssrv.Read(), err = %v", caller(), res.err)
		}
		got = strings.TrimRight(res.got, "\n")
	}

	if err == nil && wanterr != nil || err != nil && (wanterr == nil || !reflect.DeepEqual(ServerError(err), wanterr)) {
		t.Errorf("%serr = %v, wanterr = %v", caller(), err, wanterr)
	}
	if got == want {
		return
	}
	var jgot, jwant interface{}
	if err := json.Unmarshal([]byte(got), &jgot); err != nil {
		t.Errorf("%s%s(%v), output err = %v\ngot: %#q", caller(), method, in, err, got)
	}
	if err := json.Unmarshal([]byte(want), &jwant); err != nil {
		t.Errorf("%s%s(%v), expect err = %v\nexp: %#q", caller(), method, in, err, want)
	}
	if !reflect.DeepEqual(jgot, jwant) {
		t.Errorf("%s%s(%v)\nexp: %#q\ngot: %#q", caller(), method, in, want, got)
	}
}

type batchReply []interface{}

func (a batchReply) Len() int      { return len(a) }
func (a batchReply) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a batchReply) Less(i, j int) bool {
	var idI, idJ float64
	if m, ok := a[i].(map[string]interface{}); ok && m["id"] != nil {
		idI, _ = m["id"].(float64)
	}
	if m, ok := a[j].(map[string]interface{}); ok && m["id"] != nil {
		idJ, _ = m["id"].(float64)
	}
	return idI < idJ
}

func sortBatch(x interface{}) {
	if a, ok := x.([]interface{}); ok {
		sort.Sort(batchReply(a))
	}
}

// Tests

const (
	jerrParse     = `{"id":null,"error":{"code":-32700,"message":"Parse error"}}`
	jerrRequest   = `{"id":null,"error":{"code":-32600,"message":"Invalid request"}}`
	jerrMethodFmt = `{"id":0,"error":{"code":-32601,"message":"%s"}}`
	jerrParamsFmt = `{"id":0,"error":{"code":-32602,"message":"%s"}}`
	jres0         = `{"id":0,"result":0}`
	jres2         = `{"id":0,"result":2}`
	jres8         = `{"id":0,"result":8}`
	jres15        = `{"id":0,"result":15}`
)

func TestServerJSON(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// bad JSON
		{`x`, jerrParse},
		// bad Request type
		{`null`, jerrRequest},
		{`true`, jerrRequest},
		{`false`, jerrRequest},
		{`42`, jerrRequest},
		{`"str"`, jerrRequest},
		{`[]`, jerrRequest},
		{`[null]`, `[` + jerrRequest + `]`},
		{`[true]`, `[` + jerrRequest + `]`},
		{`[false]`, `[` + jerrRequest + `]`},
		{`[0]`, `[` + jerrRequest + `]`},
		{`[""]`, `[` + jerrRequest + `]`},
		{`[[]]`, `[` + jerrRequest + `]`},
		{
			`[{},[],{}]`,
			`[` + jerrRequest + `,` + jerrRequest + `,` + jerrRequest + `]`,
		},
		{
			`[{"method":""},[{"method":""}],{"method":""}]`,
			`[` + jerrRequest + `]`,
		},
		{
			`[{"id":null},[{"method":""}],{"id":0}]`,
			`[` + jerrRequest + `,` + jerrRequest + `,` + jerrRequest + `]`,
		},
		// Version
		{`{}`, jerrRequest},
		{`{"id":null,"method":"Svc.Sum","params":[3,5]}`, `{"id":null,"result":8,"error":null}`},
		{`{"id":0,"method":"Svc.Sum","params":[3,5]}`, `{"id":0,"result":8, "error":null}`},
		// Method type
		{`{"id":0,                     "params":[3,5]}`, jerrRequest},
		{`{"id":0,"method":null,       "params":[3,5]}`, jerrRequest},
		{`{"id":0,"method":true,       "params":[3,5]}`, jerrRequest},
		{`{"id":0,"method":false,      "params":[3,5]}`, jerrRequest},
		{`{"id":0,"method":42,         "params":[3,5]}`, jerrRequest},
		{`{"id":0,"method":"Svc.Sum",  "params":[3,5]}`, `{"id":0,"result":8, "error":null}`},
		{`{"id":0,"method":["Svc.Sum"],"params":[3,5]}`, jerrRequest},
		{`{"id":0,"method":{},         "params":[3,5]}`, jerrRequest},
		// // Params type
		{`{"id":0,"method":"Svc.Sum","params":null}`, jerrRequest},
		{`{"id":0,"method":"Svc.Sum","params":true}`, jerrRequest},
		{`{"id":0,"method":"Svc.Sum","params":false}`, jerrRequest},
		{`{"id":0,"method":"Svc.Sum","params":42}`, jerrRequest},
		{`{"id":0,"method":"Svc.Sum","params":"str"}`, jerrRequest},
		{`{"id":0,"method":"Svc.Sum"}`, `{"id":0,"result":0,"error":null}`},
		{`{"id":0,"method":"Svc.Sum","params":[]}`, `{"id":0,"result":0,"error":null}`},
		{
			`{"id":0,"method":"Svc.Sum","params":{}}`,
			fmt.Sprintf(jerrParamsFmt, "json: cannot unmarshal object into Go value of type [2]int"),
		},
		// Id type
		{`{"id": true, "method":"Svc.Sum","params":[]}`, jerrRequest},
		{`{"id": false,"method":"Svc.Sum","params":[]}`, jerrRequest},
		{`{"id": [0],  "method":"Svc.Sum","params":[]}`, jerrRequest},
		{`{"id": {},   "method":"Svc.Sum","params":[]}`, jerrRequest},
		{
			`{"id":null,"method":"Svc.Sum"}`,
			`{"id":null,"result":0,"error":null}`,
		},
		{
			`{"id":1,"method":"Svc.Sum"}`,
			`{"id":1,"result":0,"error":null}`,
		},
		{
			`{"id":"str", "method":"Svc.Sum"}`,
			`{"id":"str","result":0,"error":null}`,
		},
		// bad case
		{`{"id":0,"method":"Svc.Sum","params":[]}`, `{"id":0,"result":0,"error":null}`},
		{`{"ID":0,"method":"Svc.Sum","params":[]}`, `{"id":0,"result":0,"error":null}`},
		{`{"id":0,"METHOD":"Svc.Sum","params":[]}`, jerrRequest},
		// default Error.Code
		{
			`{"id":1,"method":"Svc.Err","params":{}}`,
			`{"id":1,"error":{"code":-32000,"message":"some issue"}}`,
		},
		// user-provided Error.Code
		{
			`{"id":2,"method":"Svc.Err2","params":{}}`,
			`{"id":2,"error":{"code":42,"message":"some issue"}}`,
		},
		// Error.Data
		{
			`{"id":2,"method":"Svc.Err3","params":{}}`,
			`{"id":2,"error":{"code":42,"message":"some issue","data":{"one":1,"two":2}}}`,
		},
		// net/rpc-generated errors
		{
			`{"id":0,"method":"","params":[]}`,
			fmt.Sprintf(jerrMethodFmt, "rpc: service/method request ill-formed: "),
		},
		{
			`{"id":0,"method":"Sum","params":[]}`,
			fmt.Sprintf(jerrMethodFmt, "rpc: service/method request ill-formed: Sum"),
		},
		{
			`{"id":0,"method":"Bad.Sum","params":[]}`,
			fmt.Sprintf(jerrMethodFmt, "rpc: can't find service Bad.Sum"),
		},
		{
			`{"id":0,"method":"Svc.Bad","params":[]}`,
			fmt.Sprintf(jerrMethodFmt, "rpc: can't find method Svc.Bad"),
		},
		{
			`{"id":0,"method":"Svc.Sum","params":[{}]}`,
			fmt.Sprintf(jerrParamsFmt, "json: cannot unmarshal object into Go value of type int"),
		},
		// Params to Array
		{`{"id":0,"method":"Svc.Sum"}`, `{"id":0,"result":0,"error":null}`},
		{`{"id":0,"method":"Svc.Sum","params":[]}`, `{"id":0,"result":0,"error":null}`},
		{`{"id":0,"method":"Svc.Sum","params":[8]}`, `{"id":0,"result":8,"error":null}`},
		{`{"id":0,"method":"Svc.Sum","params":[3,5]}`, `{"id":0,"result":8,"error":null}`},
		{`{"id":0,"method":"Svc.Sum","params":[3,5,7]}`, `{"id":0,"result":8,"error":null}`},
		{
			`{"id":0,"method":"Svc.Sum","params":{}}`,
			fmt.Sprintf(jerrParamsFmt, "json: cannot unmarshal object into Go value of type [2]int"),
		},
		{
			`{"id":0,"method":"Svc.Sum","params":{"a":3,"b":5}}`,
			fmt.Sprintf(jerrParamsFmt, "json: cannot unmarshal object into Go value of type [2]int"),
		},
		// Params to Slice
		{`{"id":0,"method":"Svc.SumAll"}`, `{"id":0,"result":0,"error":null}`},
		{`{"id":0,"method":"Svc.SumAll","params":[]}`, `{"id":0,"result":0,"error":null}`},
		{`{"id":0,"method":"Svc.SumAll","params":[8]}`, `{"id":0,"result":8,"error":null}`},
		{`{"id":0,"method":"Svc.SumAll","params":[3,5]}`, `{"id":0,"result":8,"error":null}`},
		{`{"id":0,"method":"Svc.SumAll","params":[3,5,7]}`, `{"id":0,"result":15,"error":null}`},
		{
			`{"id":0,"method":"Svc.SumAll","params":{}}`,
			fmt.Sprintf(jerrParamsFmt, "json: cannot unmarshal object into Go value of type []int"),
		},
		{
			`{"id":0,"method":"Svc.SumAll","params":{"a":3,"b":5}}`,
			fmt.Sprintf(jerrParamsFmt, "json: cannot unmarshal object into Go value of type []int"),
		},
		// Params to Map
		{`{"id":0,"method":"Svc.MapLen"}`, `{"id":0,"result":0,"error":null}`},
		{
			`{"id":0,"method":"Svc.MapLen","params":[]}`,
			fmt.Sprintf(jerrParamsFmt, "json: cannot unmarshal array into Go value of type map[string]int"),
		},
		{
			`{"id":0,"method":"Svc.MapLen","params":[3,5]}`,
			fmt.Sprintf(jerrParamsFmt, "json: cannot unmarshal array into Go value of type map[string]int"),
		},
		{`{"id":0,"method":"Svc.MapLen","params":{}}`, `{"id":0,"result":0,"error":null}`},
		{`{"id":0,"method":"Svc.MapLen","params":{"a":3,"b":5}}`, `{"id":0,"result":2,"error":null}`},
		// Params to Struct
		{
			`{"id":0,"method":"Svc.Name"}`,
			`{"id":0,"result":{"Name":" "},"error":null}`,
		},
		{
			`{"id":0,"method":"Svc.Name","params":[]}`,
			fmt.Sprintf(jerrParamsFmt, "json: cannot unmarshal array into Go value of type jsonrpc1.NameArg"),
		},
		{
			`{"id":0,"method":"Svc.Name","params":[3,5]}`,
			fmt.Sprintf(jerrParamsFmt, "json: cannot unmarshal array into Go value of type jsonrpc1.NameArg"),
		},
		{
			`{"id":0,"method":"Svc.Name","params":{}}`,
			`{"id":0,"result":{"Name":" "},"error":null}`,
		},
		{
			`{"id":0,"method":"Svc.Name","params":{"a":3,"b":5}}`,
			`{"id":0,"result":{"Name":" "},"error":null}`,
		},
		{
			`{"id":0,"method":"Svc.Name","params":{"Fname":"John","Lname":"Smith"}}`,
			`{"id":0,"result":{"Name":"John Smith"},"error":null}`,
		},
		// Notifications
		{
			`{"method":"Svc.Sum","params":[2,3]}` +
				`{"id":0,"method":"Svc.Sum","params":[3,5]}`,
			`{"id":0,"result":8,"error":null}`,
		},
		{
			`{"method":"Svc.Msg","params":["one"]}` +
				`{"method":"Svc.Msg","params":["two"]}` +
				`{"id":0,"method":"Svc.Sum","params":[10,5]}` +
				`{"method":"Svc.Msg","params":["three"]}`,
			`{"id":0,"result":15,"error":null}`,
		},
		// Batch
		{
			`[{"id":0,"method":"Svc.Sum","params":[2,3]}]`,
			`[{"id":0,"result":5,"error":null}]`,
		},
		{
			`[{"id":1,"method":"Svc.Sum","params":[2,3]},{"method":"Svc.Sum","params":[1,2]}]`,
			`[{"id":1,"result":5,"error":null}]`,
		},
		{
			`[{"method":"Svc.Sum","params":[1,2]},{"method":"Svc.Sum","params":[2,3]}]{"id":3,"method":"Svc.Sum","params":[3,4]}`,
			`{"id":3,"result":7,"error":null}`,
		},
		{
			`[` +
				`{"id":3,"method":"Svc.Sum","params":[3,4]},` +
				`{"id":0,"method":"Svc.Sum","params":[0,1]},` +
				`{"method":"Svc.Sum","params":[3,4]},` +
				`{"id":2,"method":"Svc.Err2"},` +
				`{"id":1,"method":"Svc.Sum","params":[1,2]}]`,
			`[` +
				`{"id":3,"result":7,"error":null},` +
				`{"id":0,"result":1,"error":null},` +
				`{"id":2,"error":{"code":42,"message":"some issue"}},` +
				`{"id":1,"result":3,"error":null}]`,
		},
	}

	for _, c := range cases {
		cli, srv := net.Pipe()
		defer cli.Close()
		go ServeConn(srv)
		buf := bufio.NewReader(cli)

		_, err := cli.Write([]byte(c.in + "\n"))
		if err != nil {
			t.Errorf("send err = %v\nsent: %#q", err, c.in)
			continue
		}
		got, err := buf.ReadString('\n')
		if err != nil {
			var jin interface{}
			if err2 := json.Unmarshal([]byte(c.in), &jin); err2 != nil {
				t.Errorf("input err = %v", err2)
			}
			t.Errorf("recv err = %v\nsent: %#q", err, c.in)
			continue
		}
		got = strings.TrimRight(got, "\n")

		var jgot, jwant interface{}
		if err := json.Unmarshal([]byte(got), &jgot); err != nil {
			t.Errorf("output err = %v\nsent: %#q\nrecv: %#q", err, c.in, got)
		}
		if err := json.Unmarshal([]byte(c.want), &jwant); err != nil {
			t.Errorf("expect err = %v\nsent: %#q\nwant: %#q", err, c.in, c.want)
		}
		sortBatch(jgot)
		sortBatch(jwant)
		if !reflect.DeepEqual(jgot, jwant) {
			t.Errorf("\nsent: %#q\nwant: %#q\nrecv: %#q", c.in, c.want, got)
		}
	}
	// Is Svc.Msg was called:
	// want := []string{"one", "two", "three"}
	// var got []string
	// for len(svcMsg) > 0 {
	// 	got = append(got, <-svcMsg)
	// }
	// sort.Strings(want)
	// sort.Strings(got)
	// if !reflect.DeepEqual(got, want) {
	// 	t.Errorf("<-svcMsg\nexp: %#q\ngot: %#q", want, got)
	// }
}

func TestClientResponse(t *testing.T) {
	var errBadResponseFmt = NewError(-32603, "bad response: %s")
	cases := []*struct {
		in      string
		want    float64
		wanterr *Error
	}{
		// // smoke
		{jres0, 0.0, errBadResponseFmt},
		// // bad JSON
		{`x`, 0.0, NewError(-32603, "invalid character 'x' looking for beginning of value")},
		// bad Response type
		{`null`, 0.0, errBadResponseFmt},
		{`true`, 0.0, errBadResponseFmt},
		{`false`, 0.0, errBadResponseFmt},
		{`42`, 0.0, errBadResponseFmt},
		{`"str"`, 0.0, errBadResponseFmt},
		{`[]`, 0.0, errBadResponseFmt},
		// Version
		{`{}`, 0.0, errBadResponseFmt},
		{`{                  "id":0,"result":0}`, 0.0, errBadResponseFmt},
		{`{"jsonrpc":null,   "id":0,"result":0}`, 0.0, errBadResponseFmt},
		{`{"jsonrpc":true,   "id":0,"result":0}`, 0.0, errBadResponseFmt},
		{`{"jsonrpc":false,  "id":0,"result":0}`, 0.0, errBadResponseFmt},
		{`{"jsonrpc":2.0,    "id":0,"result":0}`, 0.0, errBadResponseFmt},
		{`{"jsonrpc":"2.00", "id":0,"result":0}`, 0.0, errBadResponseFmt},
		{`{"jsonrpc":["2.0"],"id":0,"result":0}`, 0.0, errBadResponseFmt},
		{`{"jsonrpc":{},     "id":0,"result":0}`, 0.0, errBadResponseFmt},
		// Id type
		{`{"id":null, "result":0}`, 0.0, errBadResponseFmt},
		{jerrParse, 0.0, errParse},
		{`{"id":true, "result":0}`, 0.0, errBadResponseFmt},
		{`{"id":false,"result":0}`, 0.0, errBadResponseFmt},
		{`{"id":"0",  "result":0}`, 0.0, errBadResponseFmt},
		{`{"id":[0],  "result":0}`, 0.0, errBadResponseFmt},
		{`{"id":{},   "result":0}`, 0.0, errBadResponseFmt},
		// Result type
		{`{"id":0}`, 0.0, errBadResponseFmt},
		{`{"id":0,"result":0,"error":{"code":0,"message":""}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"result":null}`, 0.0, errBadResponseFmt},
		{
			`{"id":0,"result":true, "error":null}`,
			0.0, NewError(errInternal.Code, "json: cannot unmarshal bool into Go value of type float64"),
		},
		{
			`{"id":0,"result":false, "error":null}`,
			0.0, NewError(errInternal.Code, "json: cannot unmarshal bool into Go value of type float64"),
		},
		{`{"id":0,"result":0, "error":null}`, 0.0, nil},
		{
			`{"id":0,"result":"0", "error":null}`,
			0.0, NewError(errInternal.Code, "json: cannot unmarshal string into Go value of type float64"),
		},
		{
			`{"id":0,"result":[0], "error":null}`,
			0.0, NewError(errInternal.Code, "json: cannot unmarshal array into Go value of type float64"),
		},
		{
			`{"id":0,"result":{}, "error":null}`,
			0.0, NewError(errInternal.Code, "json: cannot unmarshal object into Go value of type float64"),
		},
		// Error type
		{`{"id":0,"error":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":true, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":false, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":0, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":"0", "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":[], "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":null, "message":""}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":true, "message":""}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":false,"message":""}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":-1,    "message":""}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":"0",  "message":""}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":[0],  "message":""}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":{},   "message":""}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":0,"message":null}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":0,"message":true}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":0,"message":false}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":0,"message":0}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":0,"message":""}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":0,"message":[]}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":0,"message":{}}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":0,"message":"","data":null}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":0,"message":"","data":"str"}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":0,"message":"","data":["str"]}, "result":null}`, 0.0, errBadResponseFmt},
		// // bad case
		{`{"id":0,"error":{"code":0,"message":"","data":""}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"ID":0,"error":{"code":0,"message":"","data":""}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"ERROR":{"code":0,"message":"","data":""}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"CODE":0,"message":"","data":""}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":0,"MESSAGE":"","data":""}, "result":null}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":0,"message":"","DATA":""}, "result":null}`, 0.0, errBadResponseFmt},
		// extra key
		{`{"id":0,"error":{"code":0,"message":"","data":"","extra":null}}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":0,"message":"","extra":null}}`, 0.0, errBadResponseFmt},
		{`{"id":0,"error":{"code":0,"message":""},"extra":null, "result":null}`, 0.0, errBadResponseFmt},
	}
	for _, c := range cases {
		if c.wanterr == errBadResponseFmt {
			c.wanterr = NewError(errBadResponseFmt.Code, fmt.Sprintf(errBadResponseFmt.Message, c.in))
		}
	}
	for _, c := range cases {
		cli, srv := net.Pipe()
		defer srv.Close()
		client := NewClient(cli)
		defer client.Close()
		buf := bufio.NewReader(srv)

		go func() {
			buf.ReadString('\n')
			if _, err := srv.Write([]byte(c.in + "\n")); err != nil {
				t.Errorf("send err = %v\nsent: %#q", err, c.in)
			}
		}()

		var got float64
		err := client.Call("method", struct{}{}, &got)
		if err == nil && c.wanterr != nil || err != nil && (c.wanterr == nil || !reflect.DeepEqual(ServerError(err), c.wanterr)) {
			t.Errorf("err = %v, wanterr = %v", err, c.wanterr)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("\n%s", dump(got, c.want))
		}
	}
}

// TODO test for rpc.ErrShutdown && io.ErrUnexpectedEOF

func TestClientRequest(t *testing.T) {
	var varSlice []int
	var varMap map[string]int
	cases := []struct {
		method  string
		in      interface{}
		want    string
		wanterr *Error
	}{
		{
			"Svc.Sum", [2]int{3, 5},
			`{"id":0,"method":"Svc.Sum","params":[3,5]}`, nil,
		},
		{
			"Svc.Err", struct{}{},
			`{"id":0,"method":"Svc.Err","params":{}}`, nil,
		},
		{
			"Svc.Err", []*struct{}{nil},
			`{"id":0,"method":"Svc.Err","params":[null]}`, nil,
		},
		{
			"", []string{""},
			`{"id":0,"method":"","params":[""]}`, nil,
		},
		{
			"", nil,
			`{"id":0,"method":""}`, nil,
		},
		{
			"", new(*int),
			``, NewError(-32603, "unsupported param type: Ptr to ptr"),
		},
		{
			"", true,
			``, NewError(-32603, "unsupported param type: bool"),
		},
		{
			"", new(bool),
			``, NewError(-32603, "unsupported param type: Ptr to bool"),
		},
		{
			"", 0,
			``, NewError(-32603, "unsupported param type: int"),
		},
		{
			"", new(int),
			``, NewError(-32603, "unsupported param type: Ptr to int"),
		},
		{
			"", [0]int{},
			`{"method":"","params":[],"id":0}`, nil,
		},
		{
			"", [2]int{1, 2},
			`{"method":"","params":[1,2],"id":0}`, nil,
		},
		{
			"", new([0]int),
			`{"method":"","params":[],"id":0}`, nil,
		},
		{
			"", new([2]int),
			`{"method":"","params":[0,0],"id":0}`, nil,
		},
		{
			"", make(chan int),
			``, NewError(-32603, "unsupported param type: chan"),
		},
		{
			"", new(chan int),
			``, NewError(-32603, "unsupported param type: Ptr to chan"),
		},
		{
			"", func() {},
			``, NewError(-32603, "unsupported param type: func"),
		},
		{
			"", new(func()),
			``, NewError(-32603, "unsupported param type: Ptr to func"),
		},
		// Looks like Go tip @ 2016-07-20 support map[int]int!
		// {
		// 	"", map[int]int{1: 2},
		// 	``, NewError(-32603, "json: unsupported type: map[int]int"),
		// },
		{
			"", map[string]int{"1": 2},
			`{"method":"","params":{"1":2},"id":0}`, nil,
		},
		{
			"", varMap,
			`{"method":"","id":0}`, nil,
		},
		// Looks like Go tip @ 2016-07-20 support map[int]int!
		// {
		// 	"", new(map[int]int),
		// 	``, NewError(-32603, "json: unsupported type: map[int]int"),
		// },
		{
			"", new(map[string]int),
			`{"method":"","id":0}`, nil,
		},
		{
			"", []int{},
			`{"method":"","params":[],"id":0}`, nil,
		},
		{
			"", []int{1, 2},
			`{"method":"","params":[1,2],"id":0}`, nil,
		},
		{
			"", varSlice,
			`{"method":"","id":0}`, nil,
		},
		{
			"", new([]int),
			`{"method":"","id":0}`, nil,
		},
		{
			"", "str",
			``, NewError(-32603, "unsupported param type: string"),
		},
		{
			"", new(string),
			``, NewError(-32603, "unsupported param type: Ptr to string"),
		},
		{
			"", struct {
				A int
				B string
			}{1, "2"},
			`{"method":"","params":{"A":1,"B":"2"},"id":0}`, nil,
		},
		{
			"", new(struct {
				A int
				B string
			}),
			`{"method":"","params":{"A":0,"B":""},"id":0}`, nil,
		},
	}

	for _, c := range cases {
		cli, srv := net.Pipe()
		defer srv.Close()
		client := NewClient(cli)
		defer client.Close()

		testClient(t, cli, srv, client, c.method, c.in, c.want, c.wanterr)
	}
}

func TestClientRequest_multi(t *testing.T) {
	cases := []struct {
		method string
		in     interface{}
		want   string
	}{
		{"Svc.Sum", [2]int{3, 5}, `{"id":0,"method":"Svc.Sum","params":[3,5]}`},
		{"Svc.Err", struct{}{}, `{"id":1,"method":"Svc.Err","params":{}}`},
		{"Svc.Sum", [2]int{3, 5}, `{"id":2,"method":"Svc.Sum","params":[3,5]}`},
	}

	cli, srv := net.Pipe()
	defer srv.Close()
	client := NewClient(cli)
	defer client.Close()

	for _, c := range cases {
		testClient(t, cli, srv, client, c.method, c.in, c.want, nil)
	}
}

func TestClientRequest_notify(t *testing.T) {
	cases := []struct {
		isNotify bool
		method   string
		in       interface{}
		want     string
		wanterr  *Error
	}{
		{
			true, "Svc.Sum", [2]int{0, 1},
			`{"method":"Svc.Sum","params":[0,1],"id":null}`, nil,
		},
		{
			false, "Svc.Sum", [2]int{3, 5},
			`{"id":0,"method":"Svc.Sum","params":[3,5]}`, nil,
		},
		{
			false, "Svc.Err", struct{}{},
			`{"id":1,"method":"Svc.Err","params":{}}`, nil,
		},
		{
			true, "Svc.Sum", [2]int{2, 3},
			`{"method":"Svc.Sum","params":[2,3],"id":null}`, nil,
		},
		{
			true, "Svc.Sum", new(int),
			"", NewError(-32603, "unsupported param type: Ptr to int"),
		},
		{
			true, "Svc.Sum", [2]int{4, 5},
			`{"method":"Svc.Sum","params":[4,5],"id":null}`, nil,
		},
		{
			false, "Svc.Sum", [2]int{3, 5},
			`{"id":2,"method":"Svc.Sum","params":[3,5]}`, nil,
		},
	}

	cli, srv := net.Pipe()
	defer srv.Close()
	client := NewClient(cli)
	defer client.Close()

	for _, c := range cases {
		if c.isNotify {
			testClientNotify(t, cli, srv, client, c.method, c.in, c.want, c.wanterr)
		} else {
			testClient(t, cli, srv, client, c.method, c.in, c.want, c.wanterr)
		}
	}
}

func TestCall(t *testing.T) {
	cases := []struct {
		method  string
		in      interface{}
		want    interface{}
		wanterr *Error
	}{
		{"Svc.Sum", [2]int{}, 0.0, nil},
		{"Svc.Sum", [2]int{3, 5}, 8.0, nil},
		{"Svc.Sum", [2]int{-3, 5}, 2.0, nil},

		{"Svc.Name", NameArg{"John", "Smith"}, map[string]interface{}{"Name": "John Smith"}, nil},

		{"Svc.Err", struct{}{}, struct{}{}, NewError(-32000, "some issue")},
		{"Svc.Err", []struct{}{}, struct{}{}, NewError(-32602, "json: cannot unmarshal array into Go value of type struct {}")},

		{"Svc.Err2", struct{}{}, struct{}{}, NewError(42, "some issue")},
	}

	for _, c := range cases {
		cli, srv := net.Pipe()
		go ServeConn(srv)
		client := NewClient(cli)
		defer client.Close()

		got := reflect.Zero(reflect.TypeOf(c.want)).Interface()
		err := client.Call(c.method, c.in, &got)
		if err == nil && c.wanterr != nil || err != nil && (c.wanterr == nil || *ServerError(err) != *c.wanterr) {
			t.Errorf("%s(%v), err = %v, wanterr = %v", c.method, c.in, err, c.wanterr)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s(%v)\n%s", c.method, c.in, dump(got, c.want))
		}
	}
}

func TestCallTyped(t *testing.T) {
	cases := []struct {
		in   NameArg
		want NameRes
	}{
		{NameArg{}, NameRes{" "}},
		{NameArg{"John", "Smith"}, NameRes{"John Smith"}},
	}

	for _, c := range cases {
		cli, srv := net.Pipe()
		go ServeConn(srv)
		client := NewClient(cli)
		defer client.Close()

		var got NameRes
		err := client.Call("Svc.Name", c.in, &got)
		if err != nil {
			t.Errorf("Svc.Name(%v), err = %v", c.in, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Svc.Name(%v)\n%s", c.in, dump(got, c.want))
		}
	}
}

func TestClientMassError_json(t *testing.T) {
	cli, srv := net.Pipe()
	go ServeConn(srv)
	client := NewClient(cli)
	defer client.Close()

	for i := len(svcMsg); i < cap(svcMsg); i++ {
		svcMsg <- ""
	}
	defer func() {
		for len(svcMsg) > 0 {
			<-svcMsg
		}
	}()

	wanterr1 := NewError(-32603, "json: cannot unmarshal number into Go value of type string")
	wanterr2 := NewError(-32603, "some other Call failed to unmarshal Reply")

	call2 := client.Go("Svc.Msg", []string{"test"}, nil, nil)
	var badreply string
	err1 := client.Call("Svc.Sum", [2]int{}, &badreply)
	if err1 == nil || !reflect.DeepEqual(ServerError(err1), wanterr1) {
		t.Errorf("%serr1 = %v, wanterr1 = %v", caller(), err1, wanterr1)
	}
	<-call2.Done
	err2 := call2.Error
	if err2 == nil || !reflect.DeepEqual(ServerError(err2), wanterr2) {
		t.Errorf("%serr2 = %v, wanterr2 = %v", caller(), err2, wanterr2)
	}
}
