package rpc

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"

	"github.com/divisionone/go-api"
	"github.com/divisionone/go-api/handler"
	proto "github.com/divisionone/go-api/internal/proto"
	"github.com/divisionone/go-micro/client"
	"github.com/divisionone/go-micro/errors"
	"github.com/divisionone/go-micro/registry"
	"github.com/divisionone/go-micro/selector"
	"github.com/divisionone/util/go/lib/ctx"
	"github.com/joncalhoun/qson"
)

const (
	Handler = "rpc"
)

type rpcHandler struct {
	opts handler.Options
	s    *api.Service
}

// strategy is a hack for selection
func strategy(services []*registry.Service) selector.Strategy {
	return func(_ []*registry.Service) selector.Next {
		// ignore input to this function, use services above
		return selector.Random(services)
	}
}

func (h *rpcHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var service *api.Service

	if h.s != nil {
		// we were given the service
		service = h.s
	} else if h.opts.Router != nil {
		// try get service from router
		s, err := h.opts.Router.Route(r)
		if err != nil {
			er := errors.InternalServerError("go.micro.api", err.Error())
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(500)
			w.Write([]byte(er.Error()))
			return
		}
		service = s
	} else {
		// we have no way of routing the request
		er := errors.InternalServerError("go.micro.api", "no route found")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		w.Write([]byte(er.Error()))
		return
	}

	// only allow post when we have the router
	if r.Method != "GET" && (h.opts.Router != nil && r.Method != "POST") {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ct := r.Header.Get("Content-Type")

	// Strip charset from Content-Type (like `application/json; charset=UTF-8`)
	if idx := strings.IndexRune(ct, ';'); idx >= 0 {
		ct = ct[:idx]
	}

	c := h.opts.Service.Client()

	// create strategy
	so := selector.WithStrategy(strategy(service.Services))

	switch ct {
	case "application/json":
		// response content type
		w.Header().Set("Content-Type", "application/json")

		br, err := requestPayloadFromRequest(r)
		if err != nil {
			e := errors.InternalServerError("go.micro.api", err.Error())
			http.Error(w, e.Error(), 500)
			return
		}

		var request json.RawMessage
		// if the extracted payload isn't empty lets use it
		if len(br) > 0 {
			request = json.RawMessage(br)
		}

		// create request/response
		var response json.RawMessage
		req := c.NewJsonRequest(service.Name, service.Endpoint.Name, &request)

		// create context
		cx := ctx.FromRequest(r)

		// make the call
		if err := c.Call(cx, req, &response, client.WithSelectOption(so)); err != nil {
			callErrString := err.Error()

			ce := errors.Parse(callErrString)
			if ce.Code == 0 {
				// assuming it's totally screwed
				ce.Code = 500
				ce.Id = "go.micro.api"
				ce.Status = http.StatusText(500)
				ce.Detail = "error during request: " + ce.Detail
			}

			w.WriteHeader(int(ce.Code))

			normalisedErr := make(map[string]interface{})
			_ = json.Unmarshal([]byte(callErrString), &normalisedErr)

			normalisedErr["code"] = ce.Code
			normalisedErr["id"] = ce.Id
			normalisedErr["status"] = ce.Status
			normalisedErr["detail"] = ce.Detail

			normalisedErrString, _ := json.Marshal(normalisedErr)

			w.Write([]byte(normalisedErrString))
			return
		}

		b, _ := response.MarshalJSON()
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.Write(b)
	case "application/proto", "application/protobuf":

		br, err := requestPayloadFromRequest(r)
		if err != nil {
			e := errors.InternalServerError("go.micro.api", err.Error())
			http.Error(w, e.Error(), 500)
			return
		}

		request := &proto.Message{}
		// if the extracted payload isn't empty lets use it
		if len(br) > 0 {
			request = proto.NewMessage(br)
		}

		// create request/response
		response := &proto.Message{}
		req := c.NewRequest(service.Name, service.Endpoint.Name, request)

		// create context
		cx := ctx.FromRequest(r)

		// make the call
		if err := c.Call(cx, req, response, client.WithSelectOption(so)); err != nil {
			ce := errors.Parse(err.Error())
			switch ce.Code {
			case 0:
				// assuming it's totally screwed
				ce.Code = 500
				ce.Id = "go.micro.api"
				ce.Status = http.StatusText(500)
				ce.Detail = "error during request: " + ce.Detail
				w.WriteHeader(500)
			default:
				w.WriteHeader(int(ce.Code))
			}

			// response content type
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(ce.Error()))
			return
		}

		b, _ := response.Marshal()
		w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.Write(b)
	default:
		http.Error(w, "unsupported content-type", 500)
		return
	}
}

func (rh *rpcHandler) String() string {
	return "rpc"
}

func NewHandler(opts ...handler.Option) handler.Handler {
	options := handler.NewOptions(opts...)
	return &rpcHandler{
		opts: options,
	}
}

func WithService(s *api.Service, opts ...handler.Option) handler.Handler {
	options := handler.NewOptions(opts...)
	return &rpcHandler{
		opts: options,
		s:    s,
	}
}

// requestPayloadFromRequest takes a *http.Request.
// If the request is a GET the query string parameters are extracted and marshaled to JSON and the raw bytes are returned.
// If the request method is a POST the request body is read and returned
func requestPayloadFromRequest(r *http.Request) ([]byte, error) {
	switch r.Method {
	case "GET":
		if len(r.URL.RawQuery) > 0 {
			return qson.ToJSON(r.URL.RawQuery)
		}
	case "POST":
		return ioutil.ReadAll(r.Body)
	}

	return []byte{}, nil
}
