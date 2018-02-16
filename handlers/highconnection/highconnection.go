package highconnection

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/nyaruka/courier"
	"github.com/nyaruka/courier/handlers"
	"github.com/nyaruka/courier/utils"
	"github.com/nyaruka/gocommon/urns"
)

/*
GET /handlers/hcnx/status/uuid?push_id=1164711372&status=6&to=%2B33611441111&ret_id=19128317&text=Msg

POST /handlers/hcnx/receive/uuid?FROM=+33644961111
ID=1164708294&FROM=%2B33644961111&TO=36105&MESSAGE=Msg&VALIDITY_DATE=2017-05-03T21%3A13%3A13&GET_STATUS=0&CLIENT=LEANCONTACTFAST&CLASS_TYPE=0&RECEPTION_DATE=2017-05-02T21%3A13%3A13&TO_OP_ID=20810&INITIAL_OP_ID=20810&STATUS=POSTING_30179_1410&EMAIL=&BINARY=0&PARAM=%7C%7C%7C%7CP223%2F03%2F03&USER_DATA=LEANCONTACTFAST&USER_DATA_2=jours+pas+r%E9gl%E9&BULK_ID=0&MO_ID=0&APPLICATION_ID=0&ACCOUNT_ID=39&GW_MESSAGE_ID=0&READ_STATUS=0&TARIFF=0&REQUEST_ID=33609002123&TAC=%28null%29&REASON=2017-05-02+23%3A13%3A13&FORMAT=&MVNO=&ORIG_ID=1164708215&ORIG_MESSAGE=Msg&RET_ID=123456&ORIG_DATE=2017-05-02T21%3A11%3A44
*/

var sendURL = "https://highpushfastapi-v2.hcnx.eu/api"
var maxMsgLength = 1500

func init() {
	courier.RegisterHandler(newHandler())
}

type handler struct {
	handlers.BaseHandler
}

func newHandler() courier.ChannelHandler {
	return &handler{handlers.NewBaseHandler(courier.ChannelType("HX"), "High Connection")}
}

// Initialize is called by the engine once everything is loaded
func (h *handler) Initialize(s courier.Server) error {
	h.SetServer(s)
	err := s.AddHandlerRoute(h, http.MethodGet, "receive", h.ReceiveMessage)
	if err != nil {
		return err
	}
	err = s.AddHandlerRoute(h, http.MethodPost, "receive", h.ReceiveMessage)
	if err != nil {
		return err
	}

	err = s.AddHandlerRoute(h, http.MethodPost, "status", h.StatusMessage)
	if err != nil {
		return err
	}

	return s.AddHandlerRoute(h, http.MethodGet, "status", h.StatusMessage)

}

type moMsg struct {
	To          string `name:"TO" validate:"required"`
	From        string `name:"FROM" validate:"required"`
	Message     string `name:"MESSAGE" validate:"required"`
	ReceiveDate string `name:"RECEPTION_DATE"`
}

// ReceiveMessage is our HTTP handler function for incoming messages
func (h *handler) ReceiveMessage(ctx context.Context, channel courier.Channel, w http.ResponseWriter, r *http.Request) ([]courier.Event, error) {
	hxRequest := &moMsg{}
	err := handlers.DecodeAndValidateForm(hxRequest, r)
	if err != nil {
		return nil, courier.WriteAndLogRequestError(ctx, w, r, channel, err)
	}

	date := time.Now()
	if hxRequest.ReceiveDate != "" {
		date, err = time.Parse("2006-01-02T15:04:05", hxRequest.ReceiveDate)
		if err != nil {
			return nil, courier.WriteAndLogRequestError(ctx, w, r, channel, err)
		}
	}

	// create our URN
	urn := urns.NewTelURNForCountry(hxRequest.From, channel.Country())

	// build our infobipMessage
	msg := h.Backend().NewIncomingMsg(channel, urn, hxRequest.Message).WithReceivedOn(date.UTC())

	// and write it
	err = h.Backend().WriteMsg(ctx, msg)
	if err != nil {
		return nil, err
	}
	return []courier.Event{msg}, courier.WriteMsgSuccess(ctx, w, r, []courier.Msg{msg})

}

type moStatus struct {
	RetID  int64 `name:"ret_id" validate:"required"`
	Status int   `name:"status" validate:"required"`
}

var statusMapping = map[int]courier.MsgStatusValue{
	2:  courier.MsgFailed,
	4:  courier.MsgSent,
	6:  courier.MsgDelivered,
	11: courier.MsgFailed,
	12: courier.MsgFailed,
	13: courier.MsgFailed,
	14: courier.MsgFailed,
	15: courier.MsgFailed,
	16: courier.MsgFailed,
}

// StatusMessage is our HTTP handler function for status updates
func (h *handler) StatusMessage(ctx context.Context, channel courier.Channel, w http.ResponseWriter, r *http.Request) ([]courier.Event, error) {
	hxRequest := &moStatus{}
	err := handlers.DecodeAndValidateForm(hxRequest, r)
	if err != nil {
		return nil, courier.WriteAndLogRequestError(ctx, w, r, channel, err)
	}

	msgStatus, found := statusMapping[hxRequest.Status]
	if !found {
		return nil, fmt.Errorf("unknown status '%d', must be one of 2, 4, 6, 11, 12, 13, 14, 15  or 16", hxRequest.Status)
	}

	// write our status
	status := h.Backend().NewMsgStatusForID(channel, courier.NewMsgID(hxRequest.RetID), msgStatus)
	err = h.Backend().WriteMsgStatus(ctx, status)
	if err == courier.ErrMsgNotFound {
		return nil, courier.WriteAndLogStatusMsgNotFound(ctx, w, r, channel)
	}

	if err != nil {
		return nil, err
	}

	return []courier.Event{status}, courier.WriteStatusSuccess(ctx, w, r, []courier.MsgStatus{status})

}

// SendMsg sends the passed in message, returning any error
func (h *handler) SendMsg(ctx context.Context, msg courier.Msg) (courier.MsgStatus, error) {
	username := msg.Channel().StringConfigForKey(courier.ConfigUsername, "")
	if username == "" {
		return nil, fmt.Errorf("no username set for HX channel")
	}

	password := msg.Channel().StringConfigForKey(courier.ConfigPassword, "")
	if password == "" {
		return nil, fmt.Errorf("no password set for HX channel")
	}

	callbackDomain := msg.Channel().CallbackDomain(h.Server().Config().Domain)
	statusURL := fmt.Sprintf("https://%s%s%s/status", callbackDomain, "/c/hx/", msg.Channel().UUID())
	receiveURL := fmt.Sprintf("https://%s%s%s/receive", callbackDomain, "/c/hx/", msg.Channel().UUID())

	status := h.Backend().NewMsgStatusForID(msg.Channel(), msg.ID(), courier.MsgErrored)
	parts := handlers.SplitMsg(handlers.GetTextAndAttachments(msg), maxMsgLength)
	for _, part := range parts {

		form := url.Values{
			"accountid":  []string{username},
			"password":   []string{password},
			"text":       []string{part},
			"to":         []string{msg.URN().Path()},
			"ret_id":     []string{msg.ID().String()},
			"datacoding": []string{"8"},
			"userdata":   []string{"textit"},
			"ret_url":    []string{statusURL},
			"ret_mo_url": []string{receiveURL},
		}

		msgURL, _ := url.Parse(sendURL)
		msgURL.RawQuery = form.Encode()

		req, err := http.NewRequest(http.MethodPost, msgURL.String(), nil)
		rr, err := utils.MakeHTTPRequest(req)

		// record our status and log
		log := courier.NewChannelLogFromRR("Message Sent", msg.Channel(), msg.ID(), rr).WithError("Message Send Error", err)
		status.AddLog(log)
		if err != nil {
			return status, nil
		}

		status.SetStatus(courier.MsgWired)

	}

	return status, nil
}
