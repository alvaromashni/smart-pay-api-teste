# Documentação da API de teste de cobrança

## Visão geral

Esta API simula cobranças para testes de comunicação entre uma PCB com ESP32 e um servidor HTTP. Ela não movimenta dinheiro, não se conecta a adquirentes e não persiste informações. Todas as cobranças desaparecem quando o processo é reiniciado.

Base URL local:

```text
http://localhost:8080
```

A interface interativa Swagger UI está disponível em `GET /docs`. A especificação OpenAPI usada por ela pode ser obtida em `GET /openapi.yaml`. A página do Swagger carrega JavaScript e CSS pela CDN `unpkg.com`, portanto o navegador precisa de acesso à internet; a API em si não precisa.

## Convenções

- Requisições e respostas usam JSON compacto.
- Valores monetários são inteiros em centavos. Por exemplo, `650` representa R$ 6,50.
- Datas são retornadas no padrão RFC 3339, em UTC.
- Toda resposta contém `Content-Length`.
- O corpo de criação está limitado a 4 KB.
- O estado é mantido somente em memória.
- Campos desconhecidos no JSON são recusados.

## Autenticação

A autenticação é opcional. Quando a variável `API_TOKEN` não está definida, as rotas são abertas. Quando está definida, todas as rotas `/v1` exigem:

```http
Authorization: Bearer <token>
```

As rotas `/health`, `/docs` e `/openapi.yaml` são públicas. Um token ausente ou incorreto resulta em HTTP `401`.

## Criar cobrança

```http
POST /v1/charges
Content-Type: application/json
```

Corpo:

| Campo | Tipo | Obrigatório | Regras |
|---|---|---:|---|
| `product_id` | texto ou inteiro | Sim | Não vazio e no máximo 64 caracteres; retorna sempre como texto |
| `value` | inteiro | Sim | De 1 a 100000 centavos; decimais e texto são recusados |

Exemplo:

```http
POST /v1/charges HTTP/1.1
Host: localhost:8080
Content-Type: application/json
Idempotency-Key: venda-0001

{"product_id":"23","value":650}
```

Resposta HTTP `201`:

```json
{"charge_id":"chg_a1b2c3d4e5f6","product_id":"23","value":650,"currency":"BRL","status":"approved","created_at":"2026-09-28T12:00:00Z"}
```

### Idempotência

O header opcional `Idempotency-Key` protege contra reenvios. Na primeira requisição, a cobrança é criada com HTTP `201`. Repetir uma requisição válida com a mesma chave devolve a cobrança original e o mesmo `charge_id`, com HTTP `200`, sem criar outra cobrança.

Uma simulação de erro não armazena a chave. Uma chave vazia é tratada como ausente.

### Simulações

| Query string | HTTP | Comportamento |
|---|---:|---|
| Ausente ou `simulate=approved` | 201 | Cria uma cobrança com `status: approved` |
| `simulate=declined` | 201 | Cria uma cobrança com `status: declined` e `reason: card_declined` |
| `simulate=error` | 500 | Retorna o erro `simulated_error` sem criar cobrança |
| `delay_ms=N` | Depende da simulação | Aguarda de 0 a 60000 milissegundos antes de responder |

Os parâmetros podem ser combinados:

```http
POST /v1/charges?simulate=declined&delay_ms=2000
```

Resposta de recusa:

```json
{"charge_id":"chg_a1b2c3d4e5f6","product_id":"23","value":650,"currency":"BRL","status":"declined","reason":"card_declined","created_at":"2026-09-28T12:00:00Z"}
```

## Listar cobranças

```http
GET /v1/charges?page=1&page_size=20
```

As cobranças são retornadas em ordem de criação, da mais antiga para a mais recente. `page` começa em 1 e `page_size` aceita valores de 1 a 100. Quando omitidos, são usados `page=1` e `page_size=20`.

Resposta HTTP `200`:

```json
{"charges":[{"charge_id":"chg_a1b2c3d4e5f6","product_id":"23","value":650,"currency":"BRL","status":"approved","created_at":"2026-09-28T12:00:00Z"}],"page":1,"page_size":20,"total":1,"total_pages":1}
```

Uma página além do final retorna `charges` vazio e preserva os metadados. Quando ainda não há cobranças, `total` e `total_pages` são zero.

## Consultar cobrança

```http
GET /v1/charges/{charge_id}
```

Exemplo:

```http
GET /v1/charges/chg_a1b2c3d4e5f6
```

Retorna HTTP `200` com a cobrança ou HTTP `404` quando ela não existe. Como o estado é volátil, cobranças criadas antes de uma reinicialização não podem mais ser consultadas.

## Verificar saúde

```http
GET /health
```

Resposta HTTP `200`:

```json
{"status":"ok"}
```

Esta rota nunca exige autenticação.

## Erros

Todos os erros seguem a mesma estrutura plana:

```json
{"error":"invalid_value","message":"value deve ser inteiro em centavos (ex.: 650 = R$ 6,50)"}
```

| HTTP | Código | Causa |
|---:|---|---|
| 400 | `invalid_json` | JSON quebrado, campo desconhecido ou mais de um valor JSON no corpo |
| 400 | `body_too_large` | Corpo maior que 4 KB |
| 401 | `unauthorized` | Token ausente ou incorreto quando a autenticação está ativa |
| 404 | `not_found` | Rota ou cobrança inexistente |
| 422 | `invalid_product_id` | Identificador ausente, vazio, de tipo incorreto ou longo demais |
| 422 | `invalid_value` | Valor ausente, decimal, textual, zero, negativo ou acima de 100000 |
| 422 | `invalid_simulate` | Simulação diferente de `approved`, `declined` ou `error` |
| 422 | `invalid_delay` | Atraso não inteiro, negativo ou acima de 60000 |
| 422 | `invalid_pagination` | Página inválida ou quantidade por página fora do intervalo de 1 a 100 |
| 500 | `simulated_error` | Erro explicitamente solicitado para teste |

## Testes rápidos

PowerShell:

```powershell
$body = @{ product_id = "23"; value = 650 } | ConvertTo-Json -Compress
Invoke-RestMethod -Method Post `
  -Uri "http://localhost:8080/v1/charges" `
  -ContentType "application/json" `
  -Headers @{ "Idempotency-Key" = "teste-001" } `
  -Body $body
```

curl:

```bash
curl -i -X POST "http://localhost:8080/v1/charges?simulate=declined" \
  -H "Content-Type: application/json" \
  -d '{"product_id":"23","value":650}'
```

## Uso do Swagger UI

1. Inicie a API com `go run .`.
2. Abra `http://localhost:8080/docs` no navegador.
3. Expanda uma operação e clique em **Try it out**.
4. Preencha os parâmetros e o corpo e clique em **Execute**.
5. Se `API_TOKEN` estiver configurada, clique em **Authorize** e informe apenas o token; o Swagger adicionará o prefixo `Bearer`.

O servidor mostrado por padrão no Swagger é `http://localhost:8080`. Para testar a partir de outro dispositivo, use diretamente o IP local do computador, como `http://192.168.1.100:8080`.

## Logs

Cada requisição gera uma linha com método, caminho completo, status, duração, IP e os primeiros 512 bytes do corpo:

```text
time=2026-09-28T09:00:00.000-03:00 level=INFO msg=requisição method=POST path="/v1/charges?simulate=declined" status=201 duration_ms=0 ip=192.168.1.50 body="{\"product_id\":\"23\",\"value\":650}"
```

Esses logs são a principal ferramenta para confirmar exatamente o que o microcontrolador enviou.
