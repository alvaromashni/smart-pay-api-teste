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

## 4. Crédito da máquina (fluxo de venda)

Cada máquina tem um **ID único** na URL. O seu ESP32 usa `99999`. O crédito é fictício e fica em memória.

O fluxo tem 3 passos:

1. **Monitorar o crédito.** O ESP32 consulta de tempos em tempos (ex.: a cada 2 s) se há crédito e quanto.
2. **Pedir a venda.** Com o produto escolhido, o ESP32 pede a venda com o preço. Se o crédito cobre, a API **reserva** o valor e responde `authorized`: pode dispensar. Se não cobre, responde `402` e nada muda.
3. **Informar o resultado.** Depois de tentar dispensar, o ESP32 avisa: `success` (o produto saiu, o crédito é consumido) ou `failed` (não saiu, o crédito volta).

Se o passo 3 não chegar em 60 s (ex.: a placa reiniciou), a reserva expira e o crédito volta sozinho.

### Consultar o crédito
```bash
curl https://api-teste.redesmartshop.com/v1/machines/99999/credit \
  -H "Authorization: Bearer <SEU_TOKEN>"
```
Resposta (`200`):
```json
{"machine_id":"99999","available":true,"value":500,"held":0,"currency":"BRL","updated_at":"2026-10-05T13:00:00Z"}
```
- `available`: `true` se há crédito.
- `value`: crédito livre, **em centavos** (`500` = R$ 5,00).
- `held`: valor reservado por uma venda que ainda aguarda resultado.

Uma máquina que nunca recebeu crédito responde `available:false` e `value:0`.

### Simular um pagamento (carregar crédito)
Isso é feito do PC, pelo `/docs` ou por curl. É o que o app de pagamento fará no sistema real.
```bash
curl -X PUT https://api-teste.redesmartshop.com/v1/machines/99999/credit \
  -H "Authorization: Bearer <SEU_TOKEN>" -H "Content-Type: application/json" \
  -d '{"value":500}'
```
Define o crédito livre da máquina. `{"value":0}` zera.

### Pedir a venda do produto escolhido
```bash
curl -X POST https://api-teste.redesmartshop.com/v1/machines/99999/vends \
  -H "Authorization: Bearer <SEU_TOKEN>" -H "Content-Type: application/json" \
  -H "Idempotency-Key: 99999-000123" \
  -d '{"product_id":"23","value":350}'
```
Autorizada (`201`). **Pode dispensar.** Guarde o `vend_id`:
```json
{"vend_id":"vnd_e402dc337732","machine_id":"99999","product_id":"23","value":350,"status":"authorized","created_at":"...","expires_at":"...","credit_remaining":150}
```
Crédito insuficiente (`402`). **Não dispense.**
```json
{"error":"insufficient_credit","message":"crédito insuficiente para este produto","machine_id":"99999","value":150,"required":350}
```
O `Idempotency-Key` é opcional, mas recomendado. Gere um por venda e repita o mesmo se precisar reenviar após um timeout: a API devolve a mesma venda (`200`) sem descontar de novo.

### Informar o resultado
```bash
curl -X POST https://api-teste.redesmartshop.com/v1/vends/vnd_e402dc337732/result \
  -H "Authorization: Bearer <SEU_TOKEN>" -H "Content-Type: application/json" \
  -d '{"result":"success"}'
```
- `success`: o produto saiu. Status `completed`; o crédito fica consumido.
- `failed`: o produto não saiu. Status `refunded`; o crédito volta para a máquina.

Reenviar o mesmo resultado responde `200` sem efeito. Se a reserva já expirou, responde `409 vend_expired`, e o crédito já voltou.

### Regras para o firmware
- **Sem resposta, sem produto.** Se a API não respondeu ou deu erro no passo 2, **não dispense**.
- **Só dispense com `201` (ou `200` do reenvio) e `status:"authorized"`.**
- **Sempre mande o resultado.** Se não mandar, o crédito volta em 60 s e o cliente pode comprar de novo sem pagar.
- Valores sempre inteiros em centavos.

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
