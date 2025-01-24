package gateways

import (
	"billing3/database"
	"billing3/service"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Paypal struct {
	httpClient     *http.Client
	returnTemplate *template.Template
}

func (p *Paypal) Settings() []GatewaySetting {
	return []GatewaySetting{
		{DisplayName: "Client ID", Name: "client_id", Type: "string", Regex: "^.+$"},
		{DisplayName: "Client Secret", Name: "client_secret", Type: "string", Regex: "^.+$"},
		{DisplayName: "Sandbox", Name: "sandbox", Type: "select", Values: []string{"Yes", "No"}},
	}
}

func (p *Paypal) Pay(invoice *database.Invoice, user *database.User, total decimal.Decimal) (string, error) {

	settings, err := getSettings(context.Background(), "Paypal")
	if err != nil {
		return "", err
	}

	paypalApi := "https://api-m.sandbox.paypal.com"
	if settings["sandbox"] == "No" {
		paypalApi = "https://api-m.paypal.com"
	}

	// obtain access token

	accessToken, err := p.getAccessToken(paypalApi, settings["client_id"], settings["client_secret"])
	if err != nil {
		return "", fmt.Errorf("get access token: %w", err)
	}

	// create order

	type amountStruct struct {
		CurrencyCode string `json:"currency_code"`
		Value        string `json:"value"`
	}
	type purchaseUnitStruct struct {
		ReferenceId string       `json:"reference_id"`
		Amount      amountStruct `json:"amount"`
	}
	type applicationContextStruct struct {
		ReturnUrl          string `json:"return_url"`
		CancelUrl          string `json:"cancel_url"`
		ShippingPreference string `json:"shipping_preference"`
		UserAction         string `json:"user_action"`
	}
	type reqStruct struct {
		PurchaseUnit       []purchaseUnitStruct     `json:"purchase_units"`
		Intent             string                   `json:"intent"`
		ApplicationContext applicationContextStruct `json:"application_context"`
	}

	req := reqStruct{
		PurchaseUnit: []purchaseUnitStruct{
			{ReferenceId: strconv.Itoa(int(invoice.ID)), Amount: amountStruct{CurrencyCode: "USD", Value: total.String()}},
		},
		Intent: "CAPTURE",
		ApplicationContext: applicationContextStruct{
			ReturnUrl:          "http://localhost:5173/api/gateway/paypal/return", // TODO
			CancelUrl:          fmt.Sprintf("http://localhost:5173/dashboard/invoice/%d", invoice.ID),
			ShippingPreference: "NO_SHIPPING",
			UserAction:         "PAY_NOW",
		},
	}

	reqBytes, err := json.Marshal(&req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}

	// send request

	httpReq, err := http.NewRequest(http.MethodPost, paypalApi+"/v2/checkout/orders", bytes.NewReader(reqBytes))
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer httpResp.Body.Close()

	// read response

	type respStruct struct {
		Id    string `json:"id"`
		Links []struct {
			Href string `json:"href"`
			Rel  string `json:"rel"`
		} `json:"links"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}

	var resp respStruct
	err = json.NewDecoder(httpResp.Body).Decode(&resp)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}

	if httpResp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("http: %s %s %s", httpResp.Status, resp.Error, resp.ErrorDescription)
	}

	slog.Info("paypal create order", "order_id", resp.Id, "invoice_id", invoice.ID, "total", total.String(), "user_id", user.ID)

	for _, link := range resp.Links {
		if link.Rel == "approve" {
			return link.Href, nil
		}
	}

	return "", fmt.Errorf("payment approve url not found")
}

// obtain access token using client id and client secret
func (p *Paypal) getAccessToken(paypalApi, clientId, clientSecret string) (string, error) {
	request, err := http.NewRequest(http.MethodPost, paypalApi+"/v1/oauth2/token", strings.NewReader("grant_type=client_credentials"))
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(clientId, clientSecret)

	httpResp, err := p.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer httpResp.Body.Close()

	type respStruct struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	var resp respStruct
	err = json.NewDecoder(httpResp.Body).Decode(&resp)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http: %s %s %s", httpResp.Status, resp.Error, resp.ErrorDescription)
	}

	return resp.AccessToken, nil
}

func (p *Paypal) Route(r chi.Router) error {

	r.Get("/return", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_ = p.returnTemplate.Execute(w, r.URL.Query().Get("token"))
	})

	r.Post("/capture", func(w http.ResponseWriter, r *http.Request) {
		settings, err := getSettings(r.Context(), "Paypal")
		if err != nil {
			slog.Error("paypal capture", "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		paypalApi := "https://api-m.sandbox.paypal.com"
		if settings["sandbox"] == "No" {
			paypalApi = "https://api-m.paypal.com"
		}

		orderId := r.PostFormValue("order_id")

		// obtain access token

		accessToken, err := p.getAccessToken(paypalApi, settings["client_id"], settings["client_secret"])
		if err != nil {
			slog.Error("paypal capture", "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// capture
		httpReq, err := http.NewRequest(http.MethodPost, paypalApi+"/v2/checkout/orders/"+orderId+"/capture", bytes.NewReader([]byte{}))
		if err != nil {
			slog.Error("paypal capture", "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+accessToken)
		httpReq.Header.Set("Prefer", "return=representation")

		httpResp, err := p.httpClient.Do(httpReq)
		if err != nil {
			slog.Error("paypal capture", "err", err, "order_id", orderId)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		defer httpResp.Body.Close()

		type amountStruct struct {
			CurrencyCode string `json:"currency_code"`
			Value        string `json:"value"`
		}
		type purchaseUnitStruct struct {
			ReferenceId string       `json:"reference_id"` // invoice id
			Amount      amountStruct `json:"amount"`
		}
		type respStruct struct {
			Id             string               `json:"id"`
			Status         string               `json:"status"`
			PurchaseUnites []purchaseUnitStruct `json:"purchase_units"`
		}

		if httpResp.StatusCode != http.StatusCreated {
			body, err := io.ReadAll(httpResp.Body)
			if err != nil {
				slog.Error("paypal capture", "err", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			slog.Error("paypal capture", "order_id", orderId, "status_code", httpResp.Status, "body", string(body))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		slog.Info("paypal capture", "order_id", orderId)

		var resp respStruct

		err = json.NewDecoder(httpResp.Body).Decode(&resp)
		if err != nil {
			slog.Error("paypal capture", "err", err, "order_id", orderId)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// add payment
		if resp.Status != "COMPLETED" {
			io.WriteString(w, "Payment incomplete")
			return
		}

		invoiceId, err := strconv.Atoi(resp.PurchaseUnites[0].ReferenceId)
		if err != nil {
			slog.Error("paypal capture", "err", "invalid invoice id", "invoice_id", resp.PurchaseUnites[0].ReferenceId)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		amount, err := decimal.NewFromString(resp.PurchaseUnites[0].Amount.Value)
		if err != nil {
			slog.Error("paypal capture", "err", "invalid amount", "amount", resp.PurchaseUnites[0].Amount.Value)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		err = service.InvoiceAddPayment(r.Context(), int32(invoiceId), "Paypal payment", amount, resp.Id, "Paypal")
		if err != nil {
			slog.Error("paypal capture", "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// redirect
		http.Redirect(w, r, fmt.Sprintf("/dashboard/invoice/%d", invoiceId), http.StatusFound)
	})
	return nil
}

func init() {
	p := Paypal{}
	p.httpClient = &http.Client{
		Timeout: time.Second * 10,
	}
	p.returnTemplate = template.Must(template.New("return").Parse(`
		<span style="font-family: sans-serif">Processing...</span>
		<form action="/api/gateway/paypal/capture" method="post">
			<input type="hidden" name="order_id" value="{{.}}">
		</form>
		<script>document.getElementsByTagName("form")[0].submit();</script>
	`))
	registerGateway("Paypal", &p)
}
