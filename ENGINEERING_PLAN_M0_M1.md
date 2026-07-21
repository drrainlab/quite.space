# Terminal Network — Engineering Plan M0–M1

**Документ:** `ENGINEERING_PLAN_M0_M1.md`
**Статус:** Draft 0.1
**Ядро:** Terminal Mesh Kernel
**Главная тема:** универсальные типизированные терминалы, capability-driven UX и архитектурная честность.

---

# 1. Главная инженерная идея

На уровне протокола **Terminal** — это любая криптографически адресуемая сущность, способная участвовать в обмене сигналами.

Terminal может представлять:

* человека;
* совместное пространство;
* чат;
* бота;
* AI-агента;
* сенсор;
* умный прибор;
* исполнительное устройство;
* локальный сервис;
* архив;
* BBS;
* relay;
* Reticulum destination;
* Meshtastic-узел;
* gateway между сетями.

Пользовательский интерфейс по-прежнему может называть совместные пространства терминалами. Но на уровне ядра пространство является одним из видов Terminal:

```text
Terminal
├── Human Terminal
├── Space Terminal
├── Bot Terminal
├── Agent Terminal
├── Sensor Terminal
├── Actuator Terminal
├── Gateway Terminal
├── Relay Terminal
└── Archive Terminal
```

Это позволяет не создавать отдельную архитектуру для людей, ботов и устройств.

Reticulum уже использует identity как абстракцию не только человека, но также машины, программы, робота или сенсора, а его destination представляет адресуемый endpoint. Это делает адаптацию к Terminal Model естественной, без искусственного сведения устройств к «пользовательским аккаунтам».

---

# 2. Основные архитектурные инварианты

Эти правила должны соблюдаться на всех этапах разработки.

## 2.1. No operator custody

Оператор Terminal Network не хранит:

* аккаунты;
* ключи;
* переписку;
* социальный граф;
* телеметрию;
* историю устройств;
* AI-контекст;
* список Terminal пользователя.

Хранение происходит только:

* локально;
* на доверенных пользователем устройствах;
* во временных зашифрованных relay;
* в добровольно выбранных архивах;
* в экспортированных пользователем bundle.

---

## 2.2. Capabilities before assumptions

Клиент не должен определять возможности Terminal по его названию, аватару или типу.

Любое действие разрешается только через явно объявленную capability.

Например:

```text
Название: Greenhouse Assistant
```

не означает, что Terminal:

* способен принимать команды;
* использует AI;
* управляет поливом;
* хранит историю;
* находится online;
* принадлежит человеку.

Все эти свойства должны объявляться отдельно.

---

## 2.3. Claims are not facts

Подписанный владельцем manifest доказывает:

> «Контролирующая сторона сделала это заявление».

Он не доказывает:

> «Это заявление объективно истинно».

Поэтому система различает:

* криптографически доказанный факт;
* самостоятельное заявление Terminal;
* наблюдаемое поведение;
* подтверждение доверенной стороны;
* локальную пользовательскую пометку;
* неизвестное состояние.

---

## 2.4. No fake certainty

UI не должен показывать:

* `online`, если известен только старый announce;
* `прочитано`, если получен только сетевой ACK;
* `доставлено адресату`, если пакет принят relay;
* `температура сейчас`, если измерению два часа;
* `человек ответил`, если сообщение создал AI;
* `безопасное устройство`, если manifest лишь заявил это;
* `удалено у всех`, если невозможно проверить чужие копии.

---

## 2.5. Transport independence

Terminal Runtime не должен знать, через какую сеть доставлен Signal.

Транспорт сообщает только:

* собственные возможности;
* ограничения;
* статус передачи;
* доступность peers;
* подтверждения, которые он действительно способен предоставить.

---

## 2.6. Graceful degradation

Каждая функция должна иметь:

* полную проекцию;
* низкополосную проекцию;
* offline-поведение;
* честное состояние `unsupported`.

Если транспорт не поддерживает действие, UI не должен имитировать его успешность.

---

## 2.7. Fail closed

При неизвестной capability, повреждённой подписи или неоднозначной политике система:

* не выполняет команду;
* не предполагает разрешение;
* не скрывает ошибку;
* сохраняет диагностическую информацию локально;
* не пересылает подозрительный payload дальше автоматически.

---

# 3. Разделение сущностей

## 3.1. Principal

Principal — субъект, контролирующий ключи.

Это может быть:

* человек;
* организация;
* устройство;
* программный агент;
* локальная группа;
* временная anonymous identity.

```yaml
principal:
  id: principal_public_key
  key_scheme: ed25519
```

Principal не обязан иметь публичное имя.

---

## 3.2. Device

Device — конкретный экземпляр оборудования или приложения.

Один Principal может контролировать несколько устройств:

```yaml
device:
  id: device_public_key
  controlled_by: principal_public_key
  status: active
```

Устройство может быть отозвано без полной смены identity.

---

## 3.3. Terminal

Terminal — адресуемый интерфейс, контролируемый Principal или групповой политикой.

Один Principal может создавать несколько Terminal:

```text
Pine Identity
├── Personal Terminal
├── Music Bot
├── Studio Sensor
└── Public Release Space
```

---

## 3.4. Space Terminal

Space Terminal — Terminal с:

* несколькими участниками;
* журналом событий;
* общей политикой;
* materialized state;
* набором Blocks;
* membership epochs.

Таким образом, пространство не является исключением из общего протокола.

---

## 3.5. Node

Node — запущенный экземпляр Terminal Mesh Kernel.

Один Node может обслуживать несколько Terminal:

```text
Raspberry Pi Node
├── Temperature Sensor Terminal
├── Store-and-forward Terminal
├── Greenhouse Space Terminal
└── Maintenance Bot Terminal
```

---

# 4. Terminal Manifest

Каждый Terminal публикует подписанный manifest.

Manifest описывает не личность в социальном смысле, а **контракт взаимодействия**.

```yaml
terminal:
  protocol_version: "0.1"

  id: terminal_public_key
  controller: principal_public_key

  kind:
    primary: sensor
    declared_by: controller

  labels:
    declared:
      - environment
      - greenhouse
      - temperature
    protocol:
      - source_only
      - machine_operated
      - no_persistent_storage

  io:
    publishes:
      - schema: observation.temperature.v1
      - schema: observation.humidity.v1

    accepts: []

    commands: []
    queries: []

  agency:
    mode: deterministic
    ai_present: false
    human_supervision: none

  storage:
    local_mode: ring_buffer
    retention_seconds: 3600
    remote_archive: none

  presence:
    announce_ttl_seconds: 300

  security:
    encrypted_output: supported
    authenticated_commands: unsupported

  manifest:
    revision: 7
    previous: manifest_hash
    signature: signature
```

Manifest должен быть:

* компактным;
* версионируемым;
* подписанным;
* пригодным для partial parsing;
* пригодным для low-bandwidth announce;
* отделённым от пользовательского профиля;
* обновляемым через новое подписанное событие.

---

# 5. Виды Terminal

`kind.primary` используется для базовой проекции UI, но не заменяет capabilities.

## 5.1. Human

Terminal, через который действует человек.

Возможные capabilities:

* принимать сообщения;
* публиковать сообщения;
* подтверждать прочтение;
* участвовать в Space;
* принимать звонки;
* управлять устройствами.

Наличие `kind: human` не доказывает, что каждое сообщение создано человеком.

---

## 5.2. Space

Совместное событийное пространство.

Возможности:

* membership;
* blocks;
* общий event log;
* shared state;
* moderation policy;
* retention policy;
* transport projections.

---

## 5.3. Bot

Детерминированная или программируемая автоматизация.

Примеры:

* RSS-бот;
* reminder;
* build bot;
* weather bridge;
* музыкальный sequencer;
* BBS bot.

Bot не обязательно использует AI.

---

## 5.4. Agent

Terminal, способный самостоятельно интерпретировать контекст и принимать решения.

Agent может быть:

* локальным;
* удалённым;
* rule-based;
* AI-powered;
* human-supervised;
* автономным.

---

## 5.5. Sensor

Источник наблюдений.

Примеры:

* температура;
* влажность;
* CO₂;
* шум;
* освещённость;
* положение;
* сердечный ритм;
* состояние батареи;
* уровень воды;
* данные мастерской.

Meshtastic уже поддерживает передачу device, environment, air-quality и некоторых health metrics, поэтому Sensor Terminal можно проецировать на существующую mesh-телеметрию, не смешивая её с обычными сообщениями.

---

## 5.6. Actuator

Terminal, способный изменить физический или цифровой мир.

Примеры:

* реле;
* свет;
* вентилятор;
* увлажнитель;
* замок;
* сервопривод;
* MIDI-инструмент;
* процесс сборки;
* локальный скрипт.

Actuator требует более строгой модели авторизации, чем Sensor.

---

## 5.7. Gateway

Преобразует события между сетями или протоколами:

* Terminal ↔ Reticulum;
* Terminal ↔ Meshtastic;
* Terminal ↔ MQTT;
* Terminal ↔ Matrix;
* Terminal ↔ email;
* Terminal ↔ BBS.

Gateway всегда должен показывать, какие свойства теряются при преобразовании.

---

## 5.8. Relay

Переносит непрозрачные пакеты, не участвуя в содержании.

Relay не должен отображаться как полноценный участник разговора.

---

## 5.9. Archive

Добровольный узел хранения.

Archive может хранить:

* зашифрованный event log;
* blobs;
* резервные копии;
* публичные Terminal.

Archive обязан объявлять:

* retention;
* quota;
* encryption mode;
* deletion semantics;
* replication policy.

---

# 6. Модель ввода и вывода

Тип Terminal и направление передачи — разные свойства.

## 6.1. Data-plane modes

```yaml
io_mode: source_only
```

Допустимые режимы:

* `source_only` — только публикует;
* `sink_only` — только принимает;
* `duplex` — публикует и принимает;
* `relay_only` — только переносит пакеты;
* `archive_only` — хранит и выдаёт разрешённые данные;
* `silent_observer` — принимает, но не публикует;
* `offline_exporter` — создаёт bundle без сетевого интерфейса.

---

## 6.2. Atomic capabilities

Режим является кратким описанием. Реальные разрешения задаются атомарно:

```yaml
capabilities:
  - signal.publish
  - signal.receive
  - query.receive
  - query.respond
  - command.receive
  - command.execute
  - object.store
  - object.serve
  - packet.relay
  - terminal.discover
  - presence.publish
```

---

## 6.3. Management plane

Source-only Sensor может не принимать пользовательские данные, но всё же нуждаться в настройке.

Поэтому data plane отделяется от management plane:

```yaml
management:
  mode: local_physical_only
```

Варианты:

* `none`;
* `local_physical_only`;
* `local_network_owner`;
* `signed_owner_commands`;
* `multi_signature`;
* `vendor_managed`.

UI обязан показывать это отдельно.

Например:

> Публикует данные наружу. Входящие сообщения не принимает. Настройка возможна только локально через USB.

---

# 7. Labels и категории

У всех Terminal есть метки, но метки не должны превращаться в неструктурированную свалку.

## 7.1. Protocol labels

Вычисляются клиентом из manifest:

```text
sys.kind.sensor
sys.io.source_only
sys.agency.deterministic
sys.storage.ephemeral
sys.transport.reticulum
sys.transport.meshtastic
```

Terminal не может произвольно назначить себе `sys.*`.

---

## 7.2. Declared labels

Подписанные заявления контролирующей стороны:

```text
declared.domain.greenhouse
declared.measurement.temperature
declared.location.mobile
declared.purpose.safety-monitoring
```

UI показывает:

> Заявлено владельцем.

---

## 7.3. Observed labels

Формируются локальным клиентом на основании поведения:

```text
observed.reachable.lan
observed.publishes.temperature
observed.response_time.slow
observed.manifest_changed
```

Наблюдение не является глобальной истиной.

---

## 7.4. Verified labels

Подписаны доверенной пользователем стороной:

```text
verified.calibrated.lab_xyz
verified.owned.community_alpha
verified.firmware.reproducible
```

Пользователь сам выбирает, каким верификаторам доверять.

---

## 7.5. Local labels

Никогда не покидают устройство без явного экспорта:

```text
local.favorite
local.untrusted
local.my_studio
local.noisy_sensor
local.ignore
```

---

## 7.6. Community labels

Метки конкретного сообщества:

```text
community.forest-net.emergency
community.forest-net.water-source
community.forest-net.public-service
```

Они не должны автоматически считаться глобальными.

---

# 8. Truth Contract

Truth Contract — обязательная часть протокола и UI.

Его задача — не установить абсолютную истину, а **не смешивать разные уровни уверенности**.

## 8.1. Уровни утверждений

Каждое важное свойство имеет origin:

```yaml
claim:
  value: calibrated
  origin: declared
  issuer: terminal_public_key
  issued_at: timestamp
  expires_at: timestamp
```

Допустимые origins:

* `protocol_derived`;
* `self_declared`;
* `peer_observed`;
* `third_party_verified`;
* `locally_assigned`;
* `unknown`.

---

## 8.2. Статусы подтверждения доставки

```text
created_local
queued
handed_to_transport
accepted_by_relay
received_by_terminal
decrypted_by_terminal
processed_by_software
presented_to_human
acknowledged_by_human
```

Клиент показывает только тот уровень, для которого получено доказательство.

Нельзя превращать:

```text
accepted_by_relay
```

в:

```text
delivered
```

---

## 8.3. Честность presence

Presence всегда содержит:

```yaml
presence:
  state: listening
  emitted_at: timestamp
  expires_at: timestamp
  source: terminal
```

После TTL UI показывает:

> Последнее известное состояние: «слушает», 17 минут назад.

А не:

> Слушает сейчас.

---

## 8.4. Честность gateway

Gateway добавляет transformation record:

```yaml
transformation:
  gateway: gateway_terminal_id
  input_schema: message.rich.v1
  output_schema: message.text.v1
  losses:
    - formatting
    - reactions
    - read_receipts
```

Если сообщение прошло через публичный или незащищённый сегмент, это должно быть видно.

---

# 9. Provenance для данных устройств

Любое измерение должно нести происхождение.

```yaml
observation:
  schema: observation.temperature.v1

  subject:
    terminal_id: greenhouse_sensor_id

  value:
    amount: 23.6
    unit: celsius

  timing:
    observed_at: timestamp
    emitted_at: timestamp

  source:
    sensor_model: declared
    channel: i2c
    calibration:
      status: self_declared
      calibrated_at: timestamp

  quality:
    precision: 0.1
    confidence: unknown
    stale_after_seconds: 600

  transformations:
    - type: moving_average
      window_seconds: 60

  synthetic: false
  simulated: false
```

UI должен различать:

* значение измерено;
* значение вычислено;
* значение предсказано;
* значение сгенерировано;
* значение введено вручную;
* значение симулируется.

---

# 10. AI-agent honesty

AI-агент всегда объявляет машинное участие.

```yaml
agency:
  mode: ai_agent
  autonomy: delegated
  human_supervision: asynchronous

  ai:
    present: true
    execution: local
    model:
      identity: declared
      name: unknown
    tools:
      - terminal.search
      - object.create
    memory:
      mode: terminal_scoped
      retention: session
```

## 10.1. Уровни автономности

* `A0 — none`: AI отсутствует.
* `A1 — assistive`: предлагает текст, человек подтверждает каждое действие.
* `A2 — delegated`: выполняет явно поставленные задачи в ограниченном scope.
* `A3 — autonomous`: самостоятельно создаёт события в пределах policy.
* `A4 — physical_control`: может управлять Actuator.

`A4` должен быть отдельным опасным классом и не входить в первоначальный production MVP.

---

## 10.2. Маркировка AI-событий

Каждое событие содержит:

```yaml
authorship:
  principal: principal_id
  produced_by: ai_agent
  human_approved: false
```

Возможные варианты:

* `human`;
* `human_with_ai_assistance`;
* `ai_agent`;
* `deterministic_bot`;
* `sensor`;
* `imported`;
* `unknown`.

---

## 10.3. Неизвестность должна оставаться неизвестностью

Если агент не раскрывает модель:

> AI-agent. Модель не указана.

Нельзя автоматически показывать конкретного провайдера или модель на основании косвенных признаков.

---

## 10.4. AI transformation chain

Если AI пересказал или изменил исходные данные:

```yaml
transformations:
  - actor: agent_terminal_id
    operation: summarize
    source_events:
      - event_hash_1
      - event_hash_2
```

Пользователь может открыть исходные события, если имеет к ним доступ.

---

# 11. Команды и физические действия

Команда Actuator должна отличаться от обычного сообщения.

```yaml
command:
  schema: actuator.switch.v1
  target: fan_terminal_id
  action: set
  parameters:
    state: on

  authorization:
    capability: command.execute.fan
    issued_by: principal_id
    expires_at: timestamp
    nonce: random

  safety:
    requires_confirmation: true
    maximum_duration_seconds: 900
```

## Обязательные ограничения

* команды подписываются;
* команды имеют expiration;
* повторное воспроизведение блокируется;
* capability ограничивается конкретным действием;
* опасные действия требуют отдельного подтверждения;
* выполнение создаёт receipt;
* отказ также создаёт receipt;
* команда не считается выполненной до подтверждения самого Actuator;
* AI не получает физические capabilities по умолчанию.

---

# 12. Signal Envelope v0

Для MVP принимается единый Signal Envelope.

```yaml
signal:
  version: 0

  id: content_hash
  terminal_id: terminal_id

  author:
    principal_id: principal_id
    device_id: device_id

  sequence: 184
  previous: previous_event_hash

  type: observation
  schema: observation.temperature.v1

  timing:
    created_at: timestamp
    logical_clock: 912

  authorship:
    produced_by: sensor
    human_approved: false

  provenance:
    source_terminal: terminal_id
    transformations: []

  payload:
    encoding: cbor
    ciphertext: bytes

  routing:
    priority: normal
    expires_at: timestamp
    max_forwards: 5

  signature: bytes
```

---

# 13. Сериализация

Рекомендуемое wire-представление MVP:

* deterministic CBOR;
* компактные integer keys на wire;
* человекочитаемый JSON/YAML diagnostic view;
* content-derived event IDs;
* signatures over canonical bytes;
* schema version в каждом payload.

CBOR рассчитан на компактное представление, ограниченные устройства и расширяемость; стандарт также определяет deterministic encoding, необходимый для стабильных подписей и content hashes. Для подписанных и зашифрованных CBOR-структур следует оценить COSE вместо создания полностью собственного контейнера. Финальный cryptographic profile фиксируется отдельным ADR после threat-model review.

---

# 14. Crypto architecture

Криптография должна быть заменяемым модулем:

```text
CryptoProvider
├── Sign
├── Verify
├── EncryptDirect
├── DecryptDirect
├── WrapGroupKey
├── RotateEpoch
├── ExportRecovery
└── DestroyKey
```

## MVP profile

Предлагаемый baseline:

* Ed25519 для подписей;
* X25519 для согласования ключей;
* современный AEAD;
* отдельный ключ устройства;
* Terminal group epoch key;
* новый epoch при изменении membership;
* group key оборачивается отдельно для каждого участника.

Собственную криптографическую конструкцию писать нельзя. Выбираются проверенные библиотеки и публикуются test vectors.

## После MVP

Group Crypto Provider должен позволять интеграцию MLS.

MLS стандартизирует асинхронное управление групповыми ключами с forward secrecy и post-compromise security для групп от двух участников до больших сообществ.

---

# 15. Terminal Mesh Kernel

```text
Terminal Mesh Kernel
├── Identity Manager
├── Terminal Registry
├── Manifest Validator
├── Capability Engine
├── Event Store
├── Schema Registry
├── Reducer Runtime
├── Sync Engine
├── Crypto Provider
├── Blob Store
├── Retention Engine
├── Trust & Claims Engine
├── Transport Manager
├── Routing Policy
└── Diagnostics
```

---

# 16. Event Store

## 16.1. Author chains

У каждого Device собственная последовательность:

```text
event 181 → event 182 → event 183
```

Преимущества:

* обнаружение пропусков;
* проверка порядка событий автора;
* защита от незаметной подмены;
* компактный sync summary.

---

## 16.2. Immutable events

Старое событие не изменяется.

Редактирование сообщения:

```text
message.created
message.revised
```

Удаление:

```text
message.tombstoned
```

Capability update:

```text
terminal.manifest.updated
```

---

## 16.3. Materialized state

Reducers строят:

* список сообщений;
* карточки;
* membership;
* capabilities;
* presence;
* состояние устройств;
* текущую сцену.

Reducer обязан быть:

* детерминированным;
* версионируемым;
* side-effect free;
* воспроизводимым из event log.

---

# 17. Sync Protocol v0

## 17.1. Handshake

Peers обмениваются:

* protocol versions;
* Terminal IDs;
* transport capabilities;
* frame limits;
* supported schemas;
* sync summary;
* compression support;
* receipt support.

---

## 17.2. Summary

```yaml
sync_summary:
  terminal_id: terminal_id

  authors:
    device_a:
      contiguous_until: 184
      exceptions: []

    device_b:
      contiguous_until: 97
      exceptions:
        - 91
```

---

## 17.3. Sync properties

Синхронизация должна быть:

* идемпотентной;
* chunked;
* resumable;
* tolerant к reordered packets;
* tolerant к duplicates;
* tolerant к temporary partitions;
* bounded по памяти;
* пригодной для store-and-forward.

---

## 17.4. Priority lanes

Очереди разделяются:

1. security и membership;
2. emergency;
3. commands и receipts;
4. короткие сообщения;
5. state patches;
6. telemetry;
7. manifests;
8. blobs.

Большой файл не должен блокировать критическое короткое событие.

---

# 18. Transport Adapter API

```go
type Transport interface {
    ID() string
    Start(ctx context.Context) error
    Stop(ctx context.Context) error

    Capabilities(ctx context.Context) TransportCapabilities
    Discover(ctx context.Context) (<-chan PeerAnnouncement, error)

    Send(ctx context.Context, envelope OutboundEnvelope) (TransferReceipt, error)
    Receive(ctx context.Context) (<-chan InboundEnvelope, error)

    PeerState(ctx context.Context, peer PeerID) PeerState
}
```

Transport не расшифровывает Terminal payload, кроме случаев, когда он является явно обозначенным Gateway.

---

# 19. Транспорты MVP

## T0 — Loopback

Для unit- и integration-тестов.

Поддерживает искусственные:

* задержки;
* потери;
* дубликаты;
* reorder;
* partitions;
* bandwidth limits.

---

## T1 — Bundle

Экспорт и импорт зашифрованного пакета:

```text
*.terminal-bundle
```

Bundle можно передать:

* флешкой;
* QR;
* email;
* Bluetooth;
* любым внешним каналом.

---

## T2 — LAN Direct

* peer discovery;
* прямое защищённое соединение;
* resumable sync;
* отсутствие обязательного coordinator.

---

## T3 — Blind Relay

Relay хранит только opaque envelopes:

```yaml
relay_item:
  destination_hint: rotating_hash
  expires_at: timestamp
  size: integer
  ciphertext: bytes
```

---

## T4 — Low-bandwidth Simulator

Профили:

* 64 bytes;
* 128 bytes;
* 240 bytes;
* высокая задержка;
* 10–30% packet loss;
* редкое окно соединения;
* отсутствие realtime.

Этот транспорт обязателен до Reticulum и Meshtastic adapters.

---

## T5 — Reticulum Adapter

Первая реализация — отдельный adapter process поверх официальной реализации Reticulum.

Mapping:

```text
Terminal Principal → Reticulum Identity
Terminal endpoint  → Reticulum Destination
Direct session     → Reticulum Link
Large object       → Reticulum Resource
```

Reticulum поддерживает destinations, identity-based endpoints, encrypted links и работу через различные сетевые интерфейсы, поэтому adapter не должен переопределять собственную маршрутизацию Reticulum.

---

## T6 — Meshtastic Adapter

Первая реализация подключается к устройству через официальный Client API.

Meshtastic использует Protocol Buffers для Bluetooth, serial/TCP API и пакетов между устройствами; собственные приложения могут использовать выделенные port numbers и передавать raw binary или protobuf payload.

Adapter поддерживает:

* короткие текстовые Signals;
* presence;
* manifests-lite;
* observations;
* delivery hints;
* store-and-forward requests;
* fragmentation только при явной поддержке.

Meshtastic Store & Forward существует как отдельная роль узла с ограничиваемым объёмом возвращаемой истории, поэтому Terminal UI обязан отличать наличие такого узла от гарантированной доставки конкретного события.

---

# 20. P2P через интернет

В M1 основной упор делается на LAN.

Интернет P2P выносится в следующий этап, поскольку устройства могут находиться за NAT и firewall.

Позднее можно использовать libp2p или эквивалентный слой с:

* peer identities;
* AutoNAT;
* hole punching;
* circuit relay;
* QUIC;
* WebRTC/WebTransport.

libp2p предоставляет NAT traversal и relay-механизмы, но relay должен оставаться необязательным и не получать доступа к Terminal payload.

---

# 21. Рекомендуемый стек MVP

## Kernel

**Go**

Причины:

* знакомый основной стек;
* хороший CLI и networking toolchain;
* простое распространение single binary;
* удобные headless nodes;
* хорошая пригодность для Raspberry Pi;
* понятная concurrency model;
* возможность изолировать UI от ядра.

## Local storage

* SQLite для индексов и materialized state;
* append-only event segments;
* content-addressed blob directory;
* OS keychain для wrapping key;
* application-level encryption для чувствительных полей.

## Desktop

* web UI;
* тонкая desktop shell;
* общение с kernel через локальный typed API;
* UI не содержит protocol logic.

## Adapters

* Go adapters там, где доступны стабильные библиотеки;
* sidecar process для Reticulum на первом этапе;
* protobuf client для Meshtastic;
* stdio или local socket adapter protocol.

## Serialization

* deterministic CBOR на wire;
* JSON diagnostic representation;
* YAML только для примеров и fixtures.

---

# 22. Репозиторий

```text
terminal-network/
├── cmd/
│   ├── terminal
│   ├── terminal-node
│   ├── terminal-relay
│   └── terminal-inspect
│
├── protocol/
│   ├── signal
│   ├── manifest
│   ├── capability
│   ├── claims
│   ├── provenance
│   ├── receipts
│   ├── schemas
│   └── codec
│
├── kernel/
│   ├── identity
│   ├── registry
│   ├── eventlog
│   ├── reducers
│   ├── sync
│   ├── crypto
│   ├── trust
│   ├── retention
│   ├── blobstore
│   └── routing
│
├── transports/
│   ├── loopback
│   ├── bundle
│   ├── lan
│   ├── relay
│   ├── simulator
│   ├── reticulum
│   └── meshtastic
│
├── terminals/
│   ├── human
│   ├── space
│   ├── bot
│   ├── agent
│   ├── sensor
│   ├── actuator
│   ├── gateway
│   └── archive
│
├── blocks/
│   ├── chat
│   ├── objects
│   ├── telemetry
│   ├── members
│   └── activity
│
├── clients/
│   ├── desktop
│   └── web-ui
│
├── specs/
├── adr/
├── testvectors/
├── simulations/
├── examples/
└── docs/
```

---

# 23. M0 — Protocol Seed

## M0.0 — Architecture Decisions

Создать ADR:

* ADR-001 Terminal ontology;
* ADR-002 identity and device keys;
* ADR-003 deterministic serialization;
* ADR-004 event identity;
* ADR-005 group encryption;
* ADR-006 local storage;
* ADR-007 transport boundary;
* ADR-008 claims and honesty model;
* ADR-009 schema evolution;
* ADR-010 deletion semantics.

### Acceptance

Ни одна базовая сущность не имеет двух конфликтующих определений.

---

## M0.1 — Protocol Types

Реализовать:

* PrincipalID;
* DeviceID;
* TerminalID;
* Signal;
* TerminalManifest;
* Capability;
* Claim;
* Provenance;
* Receipt;
* TransportCapabilities.

### Acceptance

* deterministic encoding;
* round-trip tests;
* unknown fields не ломают parsing;
* size limits;
* malformed payload rejection;
* golden test vectors.

---

## M0.2 — Identity & Signatures

Реализовать:

* создание Principal;
* device keys;
* подпись Device Principal-ключом;
* revoke event;
* encrypted recovery bundle;
* fingerprints;
* signature verification.

### Acceptance

Два независимых процесса проверяют одинаковые test vectors.

---

## M0.3 — Terminal Registry & Manifests

Реализовать:

* создание Terminal;
* manifest revisions;
* system labels;
* declared labels;
* observed labels;
* local labels;
* manifest validation;
* expiration;
* capability diff.

### Acceptance

Source-only Sensor невозможно вызвать как command receiver.

---

## M0.4 — Event Log

Реализовать:

* per-device chains;
* append;
* verify;
* deduplication;
* gaps;
* quarantine;
* replay protection;
* materialized state rebuild.

### Acceptance

Удаление локальной materialized базы и повторный replay дают идентичное состояние.

---

## M0.5 — Truth & Provenance

Реализовать:

* claim origins;
* authorship markers;
* AI markers;
* sensor freshness;
* delivery receipts;
* gateway transformation chain;
* staleness evaluation;
* UI-safe status projection.

### Acceptance

Ни один тестовый статус не повышается до более сильного без соответствующего proof-event.

---

## M0.6 — Sync Engine

Реализовать:

* sync summaries;
* missing-range requests;
* chunking;
* resume;
* duplicates;
* reorder;
* priority lanes;
* partial failure.

### Acceptance

Два offline-узла после соединения получают одинаковое состояние.

---

## M0.7 — Transport Harness

Реализовать:

* loopback;
* bundle;
* network simulator;
* adapter conformance suite.

### Acceptance

Один и тот же sync test проходит через все три транспорта без изменений kernel logic.

---

## M0.8 — Headless Terminals

Реализовать CLI-примеры:

* human;
* echo bot;
* source-only temperature sensor;
* AI-agent stub;
* sink-only logger;
* blind relay.

### Acceptance

Каждый Terminal имеет корректный manifest и не способен выполнять не заявленные операции.

---

# 24. M1 — Local-first MVP

## M1.0 — Local Storage

* encrypted local database;
* event segments;
* blob store;
* retention;
* quota;
* backup;
* restore.

---

## M1.1 — LAN Transport

* discovery;
* direct connection;
* secure session;
* sync;
* reconnect;
* diagnostics.

---

## M1.2 — Space Terminal

* private Space;
* capability invite;
* owner/member/guest;
* membership epochs;
* Chat Block;
* Activity Block;
* Object Block;
* Telemetry Block.

---

## M1.3 — Device UX

Карточка Sensor Terminal показывает:

* тип;
* заявленные capabilities;
* наблюдаемые capabilities;
* источник;
* последнее измерение;
* возраст измерения;
* unit;
* calibration status;
* retention;
* transport;
* способность принимать команды.

---

## M1.4 — Bot & Agent UX

Карточка агента показывает:

* bot или AI;
* уровень автономности;
* local или remote execution;
* human approval mode;
* доступные tools;
* memory scope;
* retention;
* физические capabilities;
* identity модели, если заявлена;
* `unknown`, если не заявлена.

---

## M1.5 — Blind Relay

* opaque queue;
* TTL;
* quota;
* rotating destination hints;
* receipts;
* automatic deletion;
* abuse limits.

---

## M1.6 — Reticulum Prototype

* sidecar adapter;
* Terminal-to-Destination mapping;
* manifest-lite announce;
* short Signals;
* direct encrypted Link;
* adapter diagnostics.

---

## M1.7 — Meshtastic Prototype

* serial/TCP connection;
* private app port;
* observation payload;
* text payload;
* manifest-lite;
* low-bandwidth projection;
* Store & Forward awareness.

---

# 25. MVP demonstration

## Demo A — Human Space

Два ноутбука:

* создают identities;
* создают приватный Space Terminal;
* соединяются по QR;
* работают offline;
* встречаются в LAN;
* синхронизируют сообщения и карточки.

---

## Demo B — Source-only Sensor

Raspberry Pi или эмулятор создаёт:

```text
Studio Climate Sensor
```

Он:

* публикует температуру и влажность;
* не принимает сообщения;
* не принимает команды;
* хранит данные только один час;
* объявляет данные как реальные или simulated;
* показывает возраст каждого наблюдения.

---

## Demo C — Honest AI Agent

Agent:

* читает сообщения только в одном Space;
* создаёт summary;
* не может приглашать участников;
* не может менять permissions;
* каждое сообщение маркирует как AI-generated;
* указывает human approval status;
* показывает scope памяти.

---

## Demo D — Blind Courier

Третий Node:

* принимает зашифрованный пакет;
* не может его прочитать;
* хранит до TTL;
* позже передаёт получателю;
* выдаёт только relay receipt.

---

## Demo E — Radio Projection

Один короткий Signal:

* создаётся в обычном Space;
* преобразуется в low-bandwidth representation;
* передаётся через Reticulum или Meshtastic;
* сохраняет автора, происхождение и тип;
* отображает потерянные свойства;
* не притворяется полным rich-message.

---

# 26. Testing strategy

## Unit tests

* codecs;
* signatures;
* manifests;
* claims;
* reducers;
* retention;
* capability checks.

## Property tests

* encode/decode stability;
* sync idempotency;
* duplicate resistance;
* reorder tolerance;
* merge convergence.

## Network simulation

* packet loss;
* duplication;
* partitions;
* long delays;
* low MTU;
* intermittent peers;
* corrupted chunks;
* malicious relay.

## Adversarial tests

* forged manifest;
* stale capability;
* revoked device;
* replayed command;
* fake receipt;
* oversized payload;
* storage exhaustion;
* event chain fork;
* AI event marked as human;
* sensor event without provenance.

## Honesty snapshot tests

Отдельные UI-тесты проверяют, что:

* relay ACK не показывается как delivery;
* stale presence не показывается как online;
* self-declared claim не показывается как verified;
* AI output не показывается как human;
* prediction не показывается как measurement;
* bridge loss отображается пользователю.

---

# 27. MVP Definition of Done

MVP готов, когда:

* нет обязательного центрального backend;
* новый пользователь создаёт identity offline;
* Space создаётся offline;
* приглашение передаётся QR или bundle;
* два узла синхронизируются через LAN;
* сообщения и объекты сходятся после partition;
* source-only Sensor работает как нативный Terminal;
* deterministic Bot работает как нативный Terminal;
* AI-agent честно маркирует события;
* blind relay не видит payload;
* один Signal проходит через low-bandwidth adapter;
* capabilities управляют UI;
* unsupported actions невозможно вызвать;
* каждый delivery status имеет проверяемую семантику;
* каждый sensor Signal содержит provenance;
* каждый AI Signal содержит authorship;
* потеря свойств при gateway conversion видна;
* оператор проекта не получает пользовательские данные.

---

# 28. Последовательность работы coding-agent

Первый инженерный цикл:

1. Создать ADR-001–ADR-010.
2. Зафиксировать Go types без networking.
3. Реализовать deterministic codec.
4. Выпустить test vectors.
5. Реализовать identity и signatures.
6. Реализовать Terminal Manifest.
7. Реализовать Capability Engine.
8. Реализовать event log.
9. Реализовать Truth Contract.
10. Реализовать reducers.
11. Реализовать sync через loopback.
12. Реализовать network simulator.
13. Добавить bundle transport.
14. Создать шесть headless Terminal.
15. Только после этого начинать LAN и UI.

Критическое правило:

> Сначала должны работать два headless-узла и source-only Sensor. Красивый интерфейс не должен маскировать незрелый протокол.

---

# 29. Итоговая инженерная формула

```text
Cryptographic Principal
+ addressable Terminal
+ signed Manifest
+ explicit Capabilities
+ typed Signals
+ provenance
+ append-only Event Log
+ deterministic State
+ transport-independent Sync
+ honest Receipts
+ optional Blind Relays
```

Terminal Network должен одинаково естественно представлять:

* человека, который разговаривает;
* комнату, которая живёт;
* датчик, который только наблюдает;
* устройство, которое только принимает команды;
* бота, который действует детерминированно;
* AI-агента, который обязан раскрывать свою машинную природу;
* relay, который переносит, но не читает;
* gateway, который честно сообщает о потерях;
* архив, который хранит только по выбранной пользователем политике.

Главный принцип:

> **Terminal никогда не должен казаться умнее, надёжнее, человечнее, безопаснее или доступнее, чем он способен доказать.**
