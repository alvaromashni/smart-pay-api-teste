# API de teste de cobrança — guia rápido

Esta API simula cobranças para você testar a comunicação HTTP entre a PCB e o servidor, **sem mexer com dinheiro de verdade**. Você manda uma cobrança, ela responde na hora com o resultado que você pedir (aprovada, recusada ou erro). Serve para você validar o fluxo de request/response do lado do microcontrolador.

## O básico

| | |
|---|---|
| **URL** | `https://api-teste.redesmartshop.com` |
| **Token** | `<SEU_TOKEN>` |
| **Doc interativa** | `https://api-teste.redesmartshop.com/docs` (abre no navegador, dá para testar clicando) |

Regras que importam para montar o request no PCB:

- Tudo é **JSON**.
- **Valor é em centavos**, número inteiro. `650` = R$ 6,50. Não mande decimal (`6.50` é recusado).
- As rotas que começam com `/v1` **exigem o token** no header:
  ```
  Authorization: Bearer <SEU_TOKEN>
  ```
- `/health` é aberta, não precisa de token.

## 1. Testar se está no ar (sem token)

```bash
curl https://api-teste.redesmartshop.com/health
```
Resposta:
```json
{"status":"ok"}
```

## 2. Criar uma cobrança

`POST /v1/charges`. O corpo só tem dois campos:

| Campo | O que é | Exemplo |
|---|---|---|
| `product_id` | id do produto (texto ou número) | `"23"` |
| `value` | valor **em centavos** | `650` |

Você escolhe o **resultado** pela query string `?simulate=`:

| Query | Responde | Status |
|---|---|---|
| `?simulate=approved` (ou sem nada) | cobrança **aprovada** | `201` |
| `?simulate=declined` | cobrança **recusada** | `201` |
| `?simulate=error` | **erro** do servidor | `500` |
| `&delay_ms=3000` | espera 3s antes de responder (para testar timeout) | — |

### Cobrança aprovada
```bash
curl -X POST "https://api-teste.redesmartshop.com/v1/charges?simulate=approved" \
  -H "Authorization: Bearer <SEU_TOKEN>" \
  -H "Content-Type: application/json" \
  -d '{"product_id":"23","value":650}'
```
Resposta (`201`):
```json
{"charge_id":"chg_a1b2c3d4e5f6","product_id":"23","value":650,"currency":"BRL","status":"approved","created_at":"2026-09-28T12:00:00Z"}
```

### Cobrança recusada
```bash
curl -X POST "https://api-teste.redesmartshop.com/v1/charges?simulate=declined" \
  -H "Authorization: Bearer <SEU_TOKEN>" \
  -H "Content-Type: application/json" \
  -d '{"product_id":"23","value":650}'
```
Resposta (`201`):
```json
{"charge_id":"chg_...","product_id":"23","value":650,"currency":"BRL","status":"declined","reason":"card_declined","created_at":"2026-09-28T12:00:00Z"}
```

### Erro do servidor (para testar como a PCB reage a falha)
```bash
curl -X POST "https://api-teste.redesmartshop.com/v1/charges?simulate=error" \
  -H "Authorization: Bearer <SEU_TOKEN>" \
  -H "Content-Type: application/json" \
  -d '{"product_id":"23","value":650}'
```
Resposta (`500`): `{"error":"simulated_error","message":"erro simulado"}`

### Resposta lenta (para testar timeout)
Segura 3 segundos antes de responder. Combine com qualquer `simulate`:
```bash
curl -X POST "https://api-teste.redesmartshop.com/v1/charges?simulate=approved&delay_ms=3000" \
  -H "Authorization: Bearer <SEU_TOKEN>" \
  -H "Content-Type: application/json" \
  -d '{"product_id":"23","value":650}'
```
`delay_ms` aceita de `0` a `60000` (até 60 segundos).

## 3. Consultar cobranças

Listar as criadas:
```bash
curl "https://api-teste.redesmartshop.com/v1/charges" \
  -H "Authorization: Bearer <SEU_TOKEN>"
```

Buscar uma pelo `charge_id` que veio na criação:
```bash
curl "https://api-teste.redesmartshop.com/v1/charges/chg_a1b2c3d4e5f6" \
  -H "Authorization: Bearer <SEU_TOKEN>"
```

## Se algo der errado

Todo erro vem no mesmo formato: `{"error":"...","message":"..."}`. Os mais comuns:

| Status | Significa |
|---:|---|
| `401` | Token faltando ou errado no header `Authorization` |
| `400 invalid_json` | JSON quebrado, ou mandou um campo a mais além de `product_id`/`value` |
| `422 invalid_value` | `value` não é inteiro em centavos (mandou decimal, texto, zero ou > 100000) |
| `422 invalid_product_id` | `product_id` vazio ou longo demais (máx. 64 caracteres) |
| `422 invalid_simulate` | `simulate` diferente de `approved`, `declined` ou `error` |

## Dois avisos práticos

1. **As cobranças ficam só na memória.** Se o servidor reiniciar, a lista zera. Para o seu teste isso não atrapalha — cada `POST` responde na hora com o resultado pedido; você não depende do histórico.
2. **Cada requisição fica registrada no log do servidor** com o corpo que chegou. Se a resposta não vier como você espera, me chama (Álvaro) que eu olho o log e te digo exatamente o que a PCB enviou — é a forma mais rápida de achar diferença entre o que você acha que mandou e o que chegou.
