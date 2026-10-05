package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed openapi.yaml
var openAPISpec []byte

const swaggerHTML = `<!doctype html>
<html lang="pt-BR">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>API de teste de cobrança</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    SwaggerUIBundle({url: "/openapi.yaml", dom_id: "#swagger-ui", deepLinking: true});
  </script>
</body>
</html>`

const (
	maxBodyBytes = 4 * 1024
	maxDelayMS   = 60_000
)

type charge struct {
	ChargeID  string `json:"charge_id"`
	ProductID string `json:"product_id"`
	Value     int64  `json:"value"`
	Currency  string `json:"currency"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	CreatedAt string `json:"created_at"`
}

type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

type chargeList struct {
	Charges    []charge `json:"charges"`
	Page       int      `json:"page"`
	PageSize   int      `json:"page_size"`
	Total      int      `json:"total"`
	TotalPages int      `json:"total_pages"`
}

type application struct {
	mu              sync.RWMutex
	charges         map[string]charge
	chargeOrder     []string
	idempotencyKeys map[string]string
	token           string
	sleep           func(time.Duration)
	now             func() time.Time
	logger          *slog.Logger
	credit          *creditStore
}

func newApplication(token string, logger *slog.Logger) *application {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &application{
		charges:         make(map[string]charge),
		idempotencyKeys: make(map[string]string),
		token:           token,
		sleep:           time.Sleep,
		now:             time.Now,
		logger:          logger,
		credit:          newCreditStore(),
	}
}

func (app *application) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", app.health)
	mux.HandleFunc("GET /docs", app.swaggerDocs)
	mux.HandleFunc("GET /openapi.yaml", app.openAPI)
	mux.HandleFunc("GET /v1/charges", app.listCharges)
	mux.HandleFunc("POST /v1/charges", app.createCharge)
	mux.HandleFunc("GET /v1/charges/{charge_id}", app.getCharge)
	mux.HandleFunc("GET /v1/machines/{machine_id}/credit", app.getCredit)
	mux.HandleFunc("PUT /v1/machines/{machine_id}/credit", app.setCredit)
	mux.HandleFunc("POST /v1/machines/{machine_id}/vends", app.createVend)
	mux.HandleFunc("GET /v1/vends/{vend_id}", app.getVend)
	mux.HandleFunc("POST /v1/vends/{vend_id}/result", app.vendResult)
	mux.HandleFunc("/", app.notFound)
	return app.logRequest(app.authenticate(mux))
}

func (app *application) swaggerDocs(w http.ResponseWriter, _ *http.Request) {
	writeContent(w, http.StatusOK, "text/html; charset=utf-8", []byte(swaggerHTML))
}

func (app *application) openAPI(w http.ResponseWriter, _ *http.Request) {
	writeContent(w, http.StatusOK, "application/yaml; charset=utf-8", openAPISpec)
}

func (app *application) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (app *application) createCharge(w http.ResponseWriter, r *http.Request) {
	simulation, delay, ok := parseSimulation(r)
	if !ok {
		if simulation == "invalid_simulate" {
			writeError(w, http.StatusUnprocessableEntity, simulation, "simulate deve ser approved, declined ou error")
		} else {
			writeError(w, http.StatusUnprocessableEntity, "invalid_delay", "delay_ms deve ser um inteiro entre 0 e 60000")
		}
		return
	}

	productID, value, validationError := decodeChargeRequest(r.Body)
	if validationError != nil {
		writeError(w, validationError.status, validationError.code, validationError.message)
		return
	}

	if delay > 0 {
		app.sleep(delay)
	}
	if simulation == "error" {
		writeError(w, http.StatusInternalServerError, "simulated_error", "erro simulado")
		return
	}

	idempotencyKey := r.Header.Get("Idempotency-Key")
	app.mu.Lock()
	if idempotencyKey != "" {
		if chargeID, exists := app.idempotencyKeys[idempotencyKey]; exists {
			existing := app.charges[chargeID]
			app.mu.Unlock()
			writeJSON(w, http.StatusOK, existing)
			return
		}
	}

	chargeID, err := newChargeID()
	if err != nil {
		app.mu.Unlock()
		app.logger.Error("falha ao gerar identificador", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "erro interno")
		return
	}
	created := charge{
		ChargeID:  chargeID,
		ProductID: productID,
		Value:     value,
		Currency:  "BRL",
		Status:    simulation,
		CreatedAt: app.now().UTC().Format(time.RFC3339),
	}
	if simulation == "declined" {
		created.Reason = "card_declined"
	}
	app.charges[chargeID] = created
	app.chargeOrder = append(app.chargeOrder, chargeID)
	if idempotencyKey != "" {
		app.idempotencyKeys[idempotencyKey] = chargeID
	}
	app.mu.Unlock()

	writeJSON(w, http.StatusCreated, created)
}

func (app *application) listCharges(w http.ResponseWriter, r *http.Request) {
	page, pageSize, ok := parsePagination(r)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "invalid_pagination", "page deve ser maior que zero e page_size deve estar entre 1 e 100")
		return
	}

	app.mu.RLock()
	total := len(app.chargeOrder)
	start := total
	if page <= total/pageSize+1 {
		start = (page - 1) * pageSize
	}
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	charges := make([]charge, 0, end-start)
	for _, chargeID := range app.chargeOrder[start:end] {
		charges = append(charges, app.charges[chargeID])
	}
	app.mu.RUnlock()

	totalPages := 0
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	writeJSON(w, http.StatusOK, chargeList{
		Charges:    charges,
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages,
	})
}

func (app *application) getCharge(w http.ResponseWriter, r *http.Request) {
	chargeID := r.PathValue("charge_id")
	app.mu.RLock()
	found, exists := app.charges[chargeID]
	app.mu.RUnlock()
	if !exists {
		writeError(w, http.StatusNotFound, "not_found", "cobrança não encontrada")
		return
	}
	writeJSON(w, http.StatusOK, found)
}

func (app *application) notFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "recurso não encontrado")
}

type requestError struct {
	status  int
	code    string
	message string
}

func decodeChargeRequest(body io.Reader) (string, int64, *requestError) {
	data, err := io.ReadAll(io.LimitReader(body, maxBodyBytes+1))
	if err != nil {
		return "", 0, &requestError{http.StatusBadRequest, "invalid_json", "não foi possível ler o corpo da requisição"}
	}
	if len(data) > maxBodyBytes {
		return "", 0, &requestError{http.StatusBadRequest, "body_too_large", "o corpo da requisição excede o limite de 4 KB"}
	}

	var payload struct {
		ProductID json.RawMessage `json:"product_id"`
		Value     json.RawMessage `json:"value"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return "", 0, &requestError{http.StatusBadRequest, "invalid_json", "o corpo deve conter um JSON válido"}
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", 0, &requestError{http.StatusBadRequest, "invalid_json", "o corpo deve conter somente um objeto JSON"}
	}

	productID, valid := parseProductID(payload.ProductID)
	if !valid {
		return "", 0, &requestError{http.StatusUnprocessableEntity, "invalid_product_id", "product_id é obrigatório e deve ser texto ou número com até 64 caracteres"}
	}
	value, valid := parseValue(payload.Value)
	if !valid {
		return "", 0, &requestError{http.StatusUnprocessableEntity, "invalid_value", "value deve ser inteiro em centavos (ex.: 650 = R$ 6,50)"}
	}
	return productID, value, nil
}

func parseProductID(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false
	}
	var text string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", false
		}
	} else {
		text = string(raw)
		if !isInteger(text) {
			return "", false
		}
	}
	return text, text != "" && len(text) <= 64
}

func parseValue(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || !isInteger(string(raw)) {
		return 0, false
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	return value, err == nil && value > 0 && value <= 100_000
}

func isInteger(value string) bool {
	if value == "" {
		return false
	}
	start := 0
	if value[0] == '-' {
		if len(value) == 1 {
			return false
		}
		start = 1
	}
	if value[start] == '0' && len(value[start:]) > 1 {
		return false
	}
	for _, character := range value[start:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func parseSimulation(r *http.Request) (string, time.Duration, bool) {
	query := r.URL.Query()
	simulations, present := query["simulate"]
	simulation := "approved"
	if present {
		if len(simulations) != 1 || simulations[0] == "" {
			return "invalid_simulate", 0, false
		}
		simulation = simulations[0]
	}
	if simulation != "approved" && simulation != "declined" && simulation != "error" {
		return "invalid_simulate", 0, false
	}

	delays, present := query["delay_ms"]
	if !present {
		return simulation, 0, true
	}
	if len(delays) != 1 || delays[0] == "" {
		return "invalid_delay", 0, false
	}
	milliseconds, err := strconv.Atoi(delays[0])
	if err != nil || milliseconds < 0 || milliseconds > maxDelayMS {
		return "invalid_delay", 0, false
	}
	return simulation, time.Duration(milliseconds) * time.Millisecond, true
}

func parsePagination(r *http.Request) (int, int, bool) {
	query := r.URL.Query()
	page, ok := positiveQueryInteger(query["page"], 1, 0)
	if !ok {
		return 0, 0, false
	}
	pageSize, ok := positiveQueryInteger(query["page_size"], 20, 100)
	if !ok {
		return 0, 0, false
	}
	return page, pageSize, true
}

func positiveQueryInteger(values []string, defaultValue, maximum int) (int, bool) {
	if len(values) == 0 {
		return defaultValue, true
	}
	if len(values) != 1 || values[0] == "" {
		return 0, false
	}
	value, err := strconv.Atoi(values[0])
	if err != nil || value <= 0 || maximum > 0 && value > maximum {
		return 0, false
	}
	return value, true
}

func newChargeID() (string, error) {
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "chg_" + hex.EncodeToString(random), nil
}

func (app *application) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if app.token == "" || !strings.HasPrefix(r.URL.Path, "/v1") {
			next.ServeHTTP(w, r)
			return
		}
		provided := r.Header.Get("Authorization")
		expected := "Bearer " + app.token
		if len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "token de autenticação ausente ou inválido")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (recorder *responseRecorder) WriteHeader(status int) {
	recorder.status = status
	recorder.ResponseWriter.WriteHeader(status)
}

func (app *application) logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		body, readError := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		loggedBody := body
		if len(loggedBody) > 512 {
			loggedBody = loggedBody[:512]
		}
		recorder := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		if readError != nil {
			writeError(recorder, http.StatusBadRequest, "invalid_json", "não foi possível ler o corpo da requisição")
		} else {
			next.ServeHTTP(recorder, r)
		}
		app.logger.Info("requisição",
			"method", r.Method,
			"path", r.URL.RequestURI(),
			"status", recorder.status,
			"duration_ms", time.Since(started).Milliseconds(),
			"ip", clientIP(r.RemoteAddr),
			"body", string(loggedBody),
		)
	})
}

func clientIP(remoteAddress string) string {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err == nil {
		return host
	}
	return remoteAddress
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		data = []byte(`{"error":"internal_error","message":"erro interno"}`)
		status = http.StatusInternalServerError
	}
	writeContent(w, status, "application/json", data)
}

func writeContent(w http.ResponseWriter, status int, contentType string, data []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	app := newApplication(os.Getenv("API_TOKEN"), logger)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	server := &http.Server{
		Addr:              "0.0.0.0:" + port,
		Handler:           app.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      65 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		logger.Info("encerrando servidor")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			logger.Error("falha no encerramento do servidor", "error", err)
		}
	}()

	logger.Info("servidor iniciado", "address", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("falha no servidor", "error", err)
		os.Exit(1)
	}
	fmt.Println("servidor encerrado")
}
