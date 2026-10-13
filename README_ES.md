# langbot-cli

[English](README.md) / [简体中文](README_CN.md) / [繁體中文](README_TW.md) / [日本語](README_JP.md) / Español / [Français](README_FR.md) / [한국어](README_KO.md) / [Русский](README_RU.md) / [Tiếng Việt](README_VI.md)

Herramienta independiente de línea de comandos de LangBot, distribuida como `lbctl`. Gestiona recursos de Workspace por HTTP, con múltiples contextos, descubrimiento de identidad y capacidades, gestión de recursos, configuración de RAG, extensiones y pruebas de Pipeline, registros de ejecución y diagnóstico de Sandbox, espera de tareas asíncronas y gestión de instalaciones locales con Docker Compose.

Utiliza un LangBot Core que proporcione:

- Autenticación con API Key en `/api/v1/system/context` y `/api/v1/system/capabilities`.
- API de Bot, Pipeline, Knowledge Base, Provider, Model, Plugin, Skill, MCP Server y tareas.
- Ocultación de secretos en consultas de Provider/Model mediante `include_secret=false`.

`lbctl` ejecuta operaciones según las capacidades, la vinculación al Workspace y los permisos declarados por Core.

## Instalación

macOS, Linux o Windows Git Bash:

```sh
curl -fsSL https://download.langbot.app/install.sh | sh
```

Windows PowerShell:

```powershell
irm https://download.langbot.app/install.ps1 | iex
```

El instalador selecciona el binario de tu plataforma y verifica SHA-256. El script de shell usa `~/.local/bin`; el de PowerShell usa `%LOCALAPPDATA%\Programs\lbctl` y lo añade al `PATH` del usuario.

Cloudflare Workers distribuye el espejo de instaladores, binarios y sumas de comprobación, sincronizado desde [GitHub Releases](https://github.com/langbot-app/langbot-cli/releases). Los archivos originales también están disponibles allí. Define `LBCTL_VERSION` para instalar una versión publicada específica.

## Compilar desde el código fuente

```sh
make build
./bin/lbctl version
./bin/lbctl --help
```

La versión base de Go es 1.26. El binario compilado se ejecuta directamente; la gestión de despliegues locales utiliza Docker Compose.

## Configurar instancias remotas

Proporciona la API Key mediante una variable de entorno o una entrada de stdin de un solo uso. La configuración se guarda en `~/.config/langbot/config.yaml` y almacena el nombre de la variable, en lugar de la clave.

Tras la configuración inicial, crea una Workspace API Key en la gestión de API Keys del panel de LangBot de destino. Los comandos autenticados detectan credenciales ausentes e indican la variable seleccionada, ejemplos PowerShell/Bash con entrada oculta y la verificación con `whoami` conservando las opciones de conexión. JSON/YAML incluye `error.reason` y `error.guidance`; el tipo sigue siendo `auth` y el código de salida 3. Se distingue una clave rechazada de una variable sin configurar; la CLI no solicita entrada automáticamente ni guarda la clave.

Primero consulta el Workspace asociado a la clave:

```sh
export LANGBOT_PRODUCTION_API_KEY='<api-key>'

LANGBOT_API_KEY="$LANGBOT_PRODUCTION_API_KEY" \
  lbctl --endpoint https://bot.example.com/langbot whoami
```

Vincula el `workspace_uuid` devuelto a un contexto:

```sh
lbctl context add production \
  --endpoint https://bot.example.com/langbot \
  --api-key-env LANGBOT_PRODUCTION_API_KEY \
  --expect-workspace '<workspace_uuid>' \
  --timeout 15s

lbctl context use production
lbctl context check production
lbctl whoami
lbctl capabilities
```

Gestionar varios destinos:

```sh
lbctl context add dev --endpoint http://localhost:5300 \
  --api-key-env LANGBOT_DEV_API_KEY \
  --expect-workspace '<workspace_uuid>'
lbctl context list
lbctl context show production
lbctl context check --all
lbctl --context dev whoami
lbctl context update production --timeout 30s
lbctl context remove dev --yes
```

Los parámetros de conexión se resuelven en este orden y se validan el destino y las credenciales.

| Parámetro | Prioridad |
|---|---|
| Context | `--context` → `LANGBOT_CONTEXT` → contexto actual |
| Endpoint | `--endpoint` → `LANGBOT_ENDPOINT` → configuración del contexto |
| Key | `--api-key-stdin` → `LANGBOT_API_KEY` → referencia a la variable del contexto |
| Tiempo de espera | `--timeout` → `LANGBOT_TIMEOUT` → configuración del contexto → 30s |

Usa `--api-key-stdin` para una invocación y `--file -` por separado para el cuerpo de la petición. Los endpoints temporales admiten lecturas controladas; las escrituras usan un contexto guardado y vinculado a un Workspace.

## Despliegue local y actualizaciones

`adopt` registra un despliegue Compose existente para su gestión local, conservando la configuración de servicios:

```sh
lbctl --endpoint http://127.0.0.1:5300 adopt \
  --file /absolute/path/docker-compose.yaml --project langbot --profile all
lbctl status
lbctl doctor
lbctl stop
lbctl start
lbctl restart
lbctl logs --follow --tail 100
```

Instala un despliegue local nuevo o desinstálalo conservando los datos:

```sh
lbctl install --version <vX.Y.Z> --profile basic --port 5300
lbctl uninstall --yes
```

`lbctl upgrade` actualiza las imágenes de los servicios LangBot. Los despliegues instalados con CLI y los adoptados usan los mismos comandos:

```sh
lbctl upgrade --version <newer-vX.Y.Z> --dry-run -o json
lbctl upgrade --version <newer-vX.Y.Z> --yes -o json
```

La actualización de un despliegue adoptado cambia las imágenes de `langbot`, `langbot_plugin_runtime` y del `langbot_box` existente, conservando puertos, montajes, variables, comentarios y otros servicios. Descarga y verifica la imagen, detiene el despliegue, respalda datos, Compose, el `.env` del proyecto si existe y el registro del despliegue, y después arranca y verifica la versión. Los respaldos y el estado de recuperación se conservan tras un fallo y se consultan con `status` y `doctor`.

La actualización automática admite servicios que declaren directamente `rockchin/langbot:<tag>`, con todos los datos en el montaje `data` del directorio Compose y SQLite, Chroma y local storage locales. Verifica configuración, alcance del respaldo y versiones antes de actualizar a una versión superior. `--dry-run` muestra el plan; la ejecución verifica la imagen. Tras editar Compose manualmente, vuelve a ejecutar `adopt` sobre el mismo destino para confirmar el nuevo resumen del archivo.

Los comandos de ciclo de vida local operan sobre la instancia desplegada. Bot, Pipeline, Skill y otros recursos usan los comandos siguientes para gestionar el Workspace del contexto.

## Gestionar recursos

Usa archivos JSON o YAML para cuerpos complejos, con los campos de la API HTTP de LangBot. `--file -` lee de stdin.

### Bot y Pipeline

```sh
lbctl bot list
lbctl bot get <uuid>
lbctl bot create --file bot.yaml
lbctl bot update <uuid> --file bot.yaml
lbctl bot delete <uuid> --yes

lbctl pipeline list
lbctl pipeline get <uuid>
lbctl pipeline apply --file pipeline.yaml
lbctl pipeline copy <uuid>
lbctl pipeline delete <uuid> --yes
```

`pipeline apply` actualiza si el cuerpo contiene `uuid`; de lo contrario, crea.

### Knowledge Base

```sh
lbctl knowledge-base list
lbctl knowledge-base get <uuid>
lbctl knowledge-base create --file knowledge-base.yaml
lbctl knowledge-base update <uuid> --file knowledge-base.yaml
lbctl knowledge-base retrieve <uuid> --file query.yaml
lbctl knowledge-base file list <uuid>
lbctl knowledge-base file delete <uuid> <file-id> --yes
lbctl knowledge-base delete <uuid> --yes
```

Sube un documento, inicia su ingestión y espera a que termine:

```sh
lbctl knowledge-base ingest <uuid> --file ./document.pdf --wait
```

### Provider y Model

```sh
lbctl provider list
lbctl provider get <uuid>
lbctl provider create --file provider.yaml
lbctl provider update <uuid> --file provider.yaml
lbctl provider scan-models <uuid> --type llm
lbctl provider delete <uuid> --yes

lbctl model list
lbctl model list --type llm --provider <provider-uuid>
lbctl model get <uuid> --type llm
lbctl model create --type llm --file model.yaml
lbctl model update <uuid> --type llm --file model.yaml
lbctl model test <uuid> --type llm
lbctl model delete <uuid> --type llm --yes
```

Los tipos de Model son `llm`, `embedding` y `rerank`. La salida aplica ocultación de secretos en el servidor y filtrado de campos en el cliente; el escaneo de modelos devuelve resultados estructurados.

### Plugin y Skill

```sh
lbctl plugin list
lbctl plugin get <author> <name>
lbctl plugin config get <author> <name>
lbctl plugin config update <author> <name> --file config.yaml
lbctl plugin install github --file plugin.yaml --wait
lbctl plugin install marketplace --file plugin.yaml --wait
lbctl plugin install local --file plugin.zip --wait
lbctl plugin upgrade <author> <name> --wait
lbctl plugin logs <author> <name>
lbctl plugin delete <author> <name> --yes --wait

lbctl skill list
lbctl skill get <name>
lbctl skill preview <name>
lbctl skill file list <name>
lbctl skill file read <name> <path>
lbctl skill file write <name> <path> --file content.md
lbctl skill install github --file skill.yaml
lbctl skill install upload --file skill.zip
lbctl skill delete <name> --yes
```

La instalación de Skill con `--dry-run` usa una vista previa del servidor. La instalación, actualización y eliminación de Plugin admiten `--wait` para seguir tareas asíncronas.

Las peticiones de plugins GitHub incluyen `owner`, `repo`, `release_tag` y `release_id`/`asset_id` o `asset_url`. Con una URL, la CLI consulta el release y envía los mismos identificadores que WebUI. La consulta a GitHub está separada de la autenticación del Workspace; dry-run realiza comprobaciones previas. Ejemplo:

```yaml
owner: langbot-app
repo: langbot-plugins
release_tag: 0.0.1.alpha.1
asset_url: https://github.com/langbot-app/langbot-plugins/releases/download/0.0.1.alpha.1/langbot-team-GoogleSearch-0.1.2.lbpkg
```

La actualización de configuración espera, durante un tiempo limitado, la lectura posterior a la recarga. La eliminación permite limpiar plugins activos e instalaciones que fallaron al arrancar.

### MCP Server

```sh
lbctl mcp-server list
lbctl mcp-server get <name>
lbctl mcp-server create --file server.yaml
lbctl mcp-server update <name> --file server.yaml
lbctl mcp-server test <name> --wait
lbctl mcp-server resources <name>
lbctl mcp-server resource-templates <name>
lbctl mcp-server resource-read <name> --file request.yaml
lbctl mcp-server logs <name>
lbctl mcp-server delete <name> --yes
```

## Escrituras y tareas

Las escrituras verifican el contexto guardado, Workspace, identidad, permisos, capacidades y requisitos de los recursos. Reglas generales:

- `--dry-run` comprueba los requisitos; la instalación de Skill usa una vista previa del servidor.
- Eliminar requiere `--yes` explícito.
- Las escrituras verificables vuelven a leer el recurso al terminar.
- Un resultado incierto devuelve `result_unknown` para inspeccionarlo antes de decidir el siguiente paso.
- Las operaciones de varios pasos conservan recursos creados o `task_id` para seguir su progreso.

Las lecturas de Bot/Pipeline conservan la configuración normal y ocultan credenciales con `***`. Las creaciones, actualizaciones y copias de Pipeline comparan campos observables al releer; las diferencias devuelven `verification_failed`. `verification.observable_fields_match` indica el resultado y `unverified_fields` enumera campos ocultos. `verified` solo es true si todos los campos comprobados son verificables y coinciden. Las actualizaciones de Bot conservan las credenciales existentes para marcadores exactos `***`; los marcadores que no puedan restaurarse con seguridad se rechazan antes de actualizar.

Consulta tareas asíncronas de forma independiente:

```sh
lbctl task list
lbctl task list --type <type> --kind <kind>
lbctl task get <task-id>
lbctl task get <task-id> --wait --wait-timeout 10m
```

La espera local y el ciclo de vida de la tarea en el servidor son independientes; continúa consultando con `task get`.

## Acceso controlado a la API

`api get/post` llama a operaciones registradas en el registro integrado:

```sh
lbctl api get '/api/v1/system/tasks?kind=plugin'
lbctl api post '/api/v1/knowledge/bases/<uuid>/retrieve' --file query.yaml
```

Usa comandos específicos para gestión de Provider/Model, instalación, pruebas y otras operaciones sensibles. `raw` ofrece lecturas compatibles de `/api/v1/system/info`.

## Salida y comprobaciones

La salida predeterminada es concisa y legible. Usa `-o json` o `-o yaml` para automatización con resultados estructurados completos. Resultados y errores van a stdout; el diagnóstico, a stderr.

`status` resume el entorno activo y conserva las advertencias de identidad y recuperación. Añade `-o json` o `-o yaml` al mismo comando para ver los ID completos de instancia/Workspace y las rutas de despliegue. Obtener la lista de capacidades no implica que todas las operaciones estén disponibles; consulta sus estados con `capabilities`.

| Código de salida | Significado |
|---|---|
| 0 | Éxito |
| 2 | Error de entrada o configuración |
| 3 / 4 / 5 | Error de autenticación / permiso denegado / no encontrado |
| 6 | Requisito incumplido o conflicto |
| 7 | Red, TLS, tiempo de espera o cancelación |
| 8 | Incompatibilidad de API o protocolo |
| 9 | Se requiere recuperar el despliegue |
| 10 | Error de servidor o interno sin clasificar |

```sh
make check
make cross-build

# Read-only diagnostics against a running Core.
LBCTL_TEST_ENDPOINT=http://localhost:5300 \
  go test ./internal/integration -run TestLiveCore -v
```

`make cross-build` genera binarios amd64/arm64 para macOS, Linux y Windows, junto con `dist/checksums.txt`.

Descubrimiento de comandos para agentes y autocompletado del shell:

```sh
lbctl schema
lbctl schema --command plugin.install.github
lbctl completion bash
```

## Descubrimiento de configuración RAG

Estos comandos usan las capacidades correspondientes de Core y requieren `resource.view`. Admiten `-o json` y `-o yaml`.

```sh
lbctl knowledge-engine list
lbctl knowledge-engine creation-schema author/engine -o yaml
lbctl knowledge-engine retrieval-schema author/engine -o json
lbctl knowledge-parser list --mime-type application/pdf
```

Selecciona un `plugin_id` de la lista de motores. El creation schema define parámetros de creación de la base de conocimiento y el retrieval schema, los de consulta. Usa `knowledge-base create`, `ingest` y `retrieve` para ingestión y recuperación.

## Vinculación de extensiones de Pipeline

```sh
lbctl pipeline extensions get PIPELINE_UUID -o json
```

`pipeline extensions update PIPELINE_UUID --file extensions.yaml` **sustituye todas las vinculaciones**. Los ocho campos son obligatorios. Consulta `extensions get` y conserva cada elemento que quieras mantener. La actualización se verifica con una nueva lectura; `--dry-run` comprueba entradas y requisitos.

El ejemplo vacía las cuatro listas de vinculaciones explícitas y desactiva todos los interruptores.

```yaml
bound_plugins: []
bound_mcp_servers: []
bound_skills: []
bound_mcp_resources: []
enable_all_plugins: false
enable_all_mcp_servers: false
enable_all_skills: false
mcp_resource_agent_read_enabled: false
```

Arrays vacíos con `enable_all_*: false` dejan el tipo de extensión sin vincular; `true` activa todas las extensiones disponibles de ese tipo. Plugin usa `{author: author, name: plugin}`; MCP Server, `uuid` de `available_mcp_servers`; Skill, `name` de `available_skills`. Un MCP Resource puede ser `{server_uuid: SERVER_UUID, uri: "docs://guide"}`. `mcp_resource_agent_read_enabled` controla que el Agent liste y lea recursos y los incluya en el contexto.

La actualización requiere un contexto guardado y vinculado, permisos `resource.manage` y `resource.view`, y la capacidad correspondiente de Core.

## Pruebas de Pipeline de un turno

```sh
lbctl pipeline run PIPELINE_UUID --message 'Responde a esta pregunta usando la base de conocimiento' --timeout 2m -o json
```

La prueba usa un contexto guardado y vinculado con `runtime.operate` y `resource.view`, y llama a los modelos y herramientas configurados. Crea una sesión nueva por ejecución. Devuelve respuesta, ID de sesión, ID de consulta e IDs de mensaje disponibles; los fallos devuelven un código distinto de cero.

Core espera hasta 60 segundos; `--timeout` controla la espera HTTP de la CLI. Un timeout indica **estado de ejecución desconocido**. Consulta los registros con IDs de mensaje/sesión en la página de monitorización de WebUI (`/home/monitoring`), o busca por Pipeline y hora tras un timeout del cliente.

Core declara `pipeline.run`; `--dry-run` comprueba los requisitos.

## Diagnóstico de registros de ejecución

Consulta registros, detalles de mensajes fallidos y errores de llamadas del Workspace asociado a la API Key actual, mediante las capacidades correspondientes de Core.

```sh
lbctl monitoring errors --pipeline PIPELINE_UUID --limit 20
lbctl monitoring messages --pipeline PIPELINE_UUID --start-time 2026-09-16T10:00:00+08:00 --end-time 2026-09-16T10:05:00+08:00
lbctl monitoring messages --session SESSION_ID --limit 20
lbctl monitoring message MESSAGE_ID
lbctl monitoring session SESSION_ID
lbctl monitoring llm-calls --pipeline PIPELINE_UUID
lbctl monitoring tool-calls --session SESSION_ID
lbctl monitoring embedding-calls --knowledge-base KB_UUID
lbctl monitoring sessions --active true
```

Las consultas requieren `resource.view`. Las listas admiten `--limit 1..500` y `--offset`, sujetas a los límites de Core. `--start-time` y `--end-time` usan ISO 8601. Consulta filtros en `--help`.

## Diagnóstico de Sandbox de solo lectura

```sh
lbctl sandbox status
lbctl sandbox sessions
lbctl sandbox errors
```

Sandbox consulta el Workspace actual: el estado requiere `resource.view`; sesiones y errores, `audit.view`. El sandbox gestionado usa comprobaciones de admisión del servidor; el estado incluye `enabled`, `available` y las explicaciones del servidor.

Core declara las capacidades correspondientes; se admite `-o json` y `-o yaml`. `enabled: false` significa desactivado; `enabled: true` con `available: false`, no disponible.
