# Tarefa: API de teste de cobrança fictícia (bancada com a PCB)

## Contexto
Estamos substituindo a UPPay (pagamentos em máquinas de vending) por um sistema próprio.
O Alexandro, engenheiro elétrico, está desenvolvendo a placa (PCB com ESP32) que vai ligar a máquina ao sistema.
Antes de qualquer integração real, ele precisa de uma API simples para testar a comunicação placa → nuvem.
Esta API é DESCARTÁVEL e de bancada: nenhum pagamento real, nenhuma integração com PagBank, nenhum banco de dados.

## Antes de codar
Entre em modo de planejamento. Leia esta spec inteira e me apresente:
1. a estrutura de arquivos;
2. as decisões que você tomaria em qualquer ponto ambíguo;
3. a lista de testes.
Só implemente depois que eu aprovar o plano.

## Stack e restrições
- Go 1.22+ usando SOMENTE a biblioteca padrão (net/http com os padrões de rota do Go 1.22, encoding/json, log/slog). Nenhuma dependência externa.
- Estado em memória (map protegido por mutex). Ao reiniciar, tudo some, e isso é esperado.
- Código e mensagens de erro em português; identificadores em inglês.
- Não crie abstrações "para o futuro" (interfaces de repositório, camadas de serviço, DI). É um arquivo de servidor e um de testes, no máximo três arquivos .go.

## Contrato

### POST /v1/charges
Request (application/json):
{"product_id":"23","value":650}

- product_id: aceitar texto OU número (normalizar para string na resposta). Obrigatório, até 64 caracteres.
- value: INTEIRO em centavos (650 = R$ 6,50). Recusar decimal (6.50), texto ("650"), zero, negativo e acima de 100000.
- Limite de corpo: 4 KB.

Resposta 201:
{"charge_id":"chg_<12 hex aleatórios>","product_id":"23","value":650,"currency":"BRL","status":"approved","created_at":"<RFC3339 UTC>"}

Simulações via query string, para testar como a placa reage:
- ?simulate=approved (padrão): 201, status "approved"
- ?simulate=declined: 201, status "declined", campo extra "reason":"card_declined"
- ?simulate=error: 500 com erro "simulated_error"
- ?delay_ms=N: espera N ms antes de responder (0 a 60000). Combina com as outras.
- Valor inválido de simulate ou delay_ms: 422.

Idempotência: se vier o header Idempotency-Key e ele já tiver sido usado, devolver a MESMA cobrança com 200 (sem criar outra).

### GET /v1/charges/{charge_id}
200 com a cobrança, ou 404.

### GET /health
200 {"status":"ok"}. Nunca exige token.

### Erros
Sempre no formato {"error":"<codigo>","message":"<texto em português>"}:
- 400 invalid_json, 400 body_too_large
- 401 unauthorized
- 404 not_found
- 422 invalid_product_id, invalid_value, invalid_simulate, invalid_delay
- 500 simulated_error
A mensagem de invalid_value deve explicar o formato: "value deve ser inteiro em centavos (ex.: 650 = R$ 6,50)".

## Requisitos para o cliente ser um microcontrolador (ESP32)
- JSON compacto (sem indentação) e header Content-Length SEMPRE presente em toda resposta.
- Campos planos, sem objetos aninhados.
- Servidor escuta em todas as interfaces (0.0.0.0), porta vinda da env PORT (padrão 8080).
- WriteTimeout do servidor maior que o delay máximo de simulação (60 s).

## Autenticação opcional
Se a env API_TOKEN estiver definida, as rotas /v1 exigem "Authorization: Bearer <token>" (comparação em tempo constante). Sem a env, rotas abertas.

## Logs
Uma linha por requisição (slog em formato texto): method, path com query, status, duração em ms, IP de origem e o CORPO recebido (truncado em 512 bytes).
Esse log é a principal ferramenta de depuração na bancada: é onde vamos ver exatamente o que a placa enviou.

## Encerramento
Graceful shutdown com SIGINT/SIGTERM (timeout de 10 s).

## Testes (obrigatórios)
Testes com httptest, nomes em português no estilo TestDeveFazerAlgoQuandoCondicao. No mínimo:
- cria cobrança aprovada com payload válido (confere campos, prefixo chg_ e Content-Length);
- aceita product_id numérico;
- tabela de payloads inválidos: JSON quebrado, sem product_id, sem value, value decimal, value como texto, zero, negativo (confere status E código de erro);
- simulate=declined e simulate=error;
- delay_ms: injete a função de sleep para o teste NÃO esperar de verdade; confira que recebeu a duração certa e que delay acima do limite dá 422;
- mesma Idempotency-Key devolve o mesmo charge_id com 200;
- token exigido quando API_TOKEN está definido;
- GET por id (200 e 404).

## Entregáveis
- Código Go + testes
- Dockerfile multi-stage (build em golang:alpine, imagem final distroless static, usuário nonroot)
- .gitignore
- README.md em português com: como rodar (go run e Docker), variáveis de ambiente, contrato completo com exemplos, tabela de simulações, tabela de erros, exemplo de linha de log, exemplo curl, e um exemplo mínimo para ESP32 em Arduino usando HTTPClient + ArduinoJson 7 (timeout de 15 s, tratar falha de conexão, JSON inválido, aprovada e não aprovada). Explique no README como a placa alcança o PC na mesma rede (IP local + liberar a porta no firewall).

## Critérios de aceite
- `gofmt -l .` sem saída, `go vet ./...` limpo, `go test ./...` passando.
- Rodar o servidor e executar via curl: um caso aprovado, um declined, um com value decimal. Me mostre as respostas e as linhas de log correspondentes.
- Só diga que terminou depois de rodar tudo isso.

## Fora do escopo (não faça)
Banco de dados, HTTPS, integração com PagBank, rate limit, métricas, frameworks, múltiplos pacotes.