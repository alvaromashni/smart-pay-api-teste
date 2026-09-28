# API de teste de cobrança

API descartável de bancada para testar a comunicação entre uma PCB com ESP32 e a nuvem. Não realiza pagamentos reais e mantém todas as cobranças somente em memória.

## Como executar

Requer Go 1.22 ou mais recente:

```bash
go run .
```

Com Docker:

```bash
docker build -t api-teste .
docker run --rm -p 8080:8080 api-teste
```

O servidor escuta em todas as interfaces. As variáveis disponíveis são:

| Variável | Padrão | Descrição |
|---|---:|---|
| `PORT` | `8080` | Porta HTTP do servidor |
| `API_TOKEN` | vazio | Se definida, exige autenticação Bearer nas rotas `/v1` |

Todo estado é perdido quando o processo reinicia.

## Documentação interativa

Com o servidor em execução, abra `http://localhost:8080/docs` para usar o Swagger UI. A interface carrega seus recursos visuais pela CDN `unpkg.com` e usa a especificação servida em `http://localhost:8080/openapi.yaml`.

Uma referência textual detalhada também está disponível em [`api-doc.md`](api-doc.md).

## Contrato

Todas as respostas são JSON compacto e incluem `Content-Length`.

### Criar cobrança

`POST /v1/charges`

```json
{"product_id":"23","value":650}
```

`product_id` é obrigatório, aceita texto ou número e tem no máximo 64 caracteres. `value` é obrigatório e deve ser um inteiro entre 1 e 100000, em centavos.

Resposta `201 Created`:

```json
{"charge_id":"chg_a1b2c3d4e5f6","product_id":"23","value":650,"currency":"BRL","status":"approved","created_at":"2026-09-26T15:00:00Z"}
```

O header opcional `Idempotency-Key` evita duplicidade. Quando a chave já foi utilizada, a API devolve a cobrança original com status `200 OK`.

### Consultar cobrança

`GET /v1/charges/{charge_id}` retorna a cobrança com `200 OK`, ou `404 Not Found` quando ela não existe.

### Listar cobranças

`GET /v1/charges?page=1&page_size=20` retorna as cobranças em ordem de criação. `page` começa em 1, `page_size` aceita de 1 a 100 e os valores padrão são 1 e 20.

```json
{"charges":[],"page":1,"page_size":20,"total":0,"total_pages":0}
```

### Saúde

`GET /health` retorna:

```json
{"status":"ok"}
```

Essa rota nunca exige token.

### Autenticação

Quando `API_TOKEN` estiver definida, envie nas rotas `/v1`:

```text
Authorization: Bearer seu-token
```

### Simulações

Os parâmetros podem ser combinados, por exemplo `?simulate=declined&delay_ms=1000`.

| Parâmetro | Resultado |
|---|---|
| `simulate=approved` | Padrão; cria cobrança aprovada com HTTP 201 |
| `simulate=declined` | Cria cobrança recusada com HTTP 201 e `reason: card_declined` |
| `simulate=error` | Responde HTTP 500 com `simulated_error` |
| `delay_ms=N` | Aguarda de 0 a 60000 ms antes da resposta |

### Erros

Todos seguem o formato `{"error":"codigo","message":"mensagem em português"}`.

| HTTP | Código | Situação |
|---:|---|---|
| 400 | `invalid_json` | JSON inválido, campos desconhecidos ou conteúdo adicional |
| 400 | `body_too_large` | Corpo acima de 4 KB |
| 401 | `unauthorized` | Token ausente ou inválido |
| 404 | `not_found` | Rota ou cobrança inexistente |
| 422 | `invalid_product_id` | `product_id` ausente ou inválido |
| 422 | `invalid_value` | `value` ausente ou inválido |
| 422 | `invalid_simulate` | Simulação desconhecida |
| 422 | `invalid_delay` | Atraso inválido ou acima de 60000 ms |
| 422 | `invalid_pagination` | Paginação inválida |
| 500 | `simulated_error` | Erro solicitado para teste |

## Exemplos com curl

```bash
curl -i -X POST http://localhost:8080/v1/charges \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: venda-123" \
  -d '{"product_id":"23","value":650}'
```

Com token:

```bash
curl -i -X POST http://localhost:8080/v1/charges \
  -H "Authorization: Bearer seu-token" \
  -H "Content-Type: application/json" \
  -d '{"product_id":23,"value":650}'
```

Cada requisição produz uma linha de log para depuração. O corpo é limitado a 512 bytes no log:

```text
time=2026-09-26T12:00:00.000-03:00 level=INFO msg=requisição method=POST path="/v1/charges?simulate=declined" status=201 duration_ms=0 ip=192.168.1.50 body="{\"product_id\":\"23\",\"value\":650}"
```
