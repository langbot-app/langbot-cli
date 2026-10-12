# langbot-cli

[English](README.md) / [简体中文](README_CN.md) / [繁體中文](README_TW.md) / [日本語](README_JP.md) / [Español](README_ES.md) / Français / [한국어](README_KO.md) / [Русский](README_RU.md) / [Tiếng Việt](README_VI.md)

Outil autonome en ligne de commande de LangBot, fourni sous le nom `lbctl`. Il gère les ressources Workspace par HTTP : contextes multiples, découverte d'identité et de capacités, ressources, configuration RAG, extensions et essais Pipeline, historiques d'exécution et diagnostic Sandbox, attente de tâches asynchrones, ainsi que l'installation, l'adoption, le cycle de vie et la mise à niveau de déploiements Docker Compose locaux.

Utilisez une version de LangBot Core fournissant :

- L'authentification API Key pour `/api/v1/system/context` et `/api/v1/system/capabilities`.
- Les API Bot, Pipeline, Knowledge Base, Provider, Model, Plugin, Skill, MCP Server et tâches.
- Le masquage côté serveur via `include_secret=false` pour Provider/Model.

`lbctl` exécute les opérations selon les capacités, les liens Workspace et les permissions déclarés par Core.

## Installation

macOS, Linux ou Windows Git Bash :

```sh
curl -fsSL https://download.langbot.app/install.sh | sh
```

Windows PowerShell :

```powershell
irm https://download.langbot.app/install.ps1 | iex
```

L'installateur sélectionne le binaire de votre plateforme et vérifie SHA-256. Le script shell utilise `~/.local/bin` ; PowerShell utilise `%LOCALAPPDATA%\Programs\lbctl` et l'ajoute au `PATH` utilisateur.

Cloudflare Workers distribue le miroir des installateurs, binaires et sommes de contrôle, synchronisé depuis [GitHub Releases](https://github.com/langbot-app/langbot-cli/releases), où les fichiers originaux restent disponibles. Définissez `LBCTL_VERSION` pour installer une version publiée précise.

## Compilation depuis les sources

```sh
make build
./bin/lbctl version
./bin/lbctl --help
```

La version de référence de Go est 1.26. Le binaire compilé s'exécute directement ; la gestion des déploiements locaux utilise Docker Compose.

## Configurer les instances distantes

Fournissez l'API Key par variable d'environnement ou par une entrée stdin ponctuelle. La configuration est stockée dans `~/.config/langbot/config.yaml` et conserve le nom de la variable plutôt que la clé.

Identifiez d'abord le Workspace associé à la clé :

```sh
export LANGBOT_PRODUCTION_API_KEY='<api-key>'

LANGBOT_API_KEY="$LANGBOT_PRODUCTION_API_KEY" \
  lbctl --endpoint https://bot.example.com/langbot whoami
```

Liez le `workspace_uuid` retourné à un contexte :

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

Gérer plusieurs destinations :

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

Les paramètres suivent cet ordre de priorité ; la destination et les identifiants sélectionnés sont vérifiés.

| Paramètre | Priorité |
|---|---|
| Context | `--context` → `LANGBOT_CONTEXT` → contexte courant |
| Endpoint | `--endpoint` → `LANGBOT_ENDPOINT` → configuration du contexte |
| Key | `--api-key-stdin` → `LANGBOT_API_KEY` → variable référencée par le contexte |
| Délai | `--timeout` → `LANGBOT_TIMEOUT` → configuration du contexte → 30s |

Utilisez `--api-key-stdin` pour un appel unique et `--file -` séparément pour le corps de requête. Les endpoints temporaires permettent des lectures contrôlées ; les écritures utilisent un contexte enregistré et lié au Workspace.

## Déploiement local et mises à niveau

`adopt` enregistre un déploiement Compose existant pour la gestion locale, en préservant sa configuration :

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

Installez un déploiement local ou désinstallez-le en conservant ses données :

```sh
lbctl install --version <vX.Y.Z> --profile basic --port 5300
lbctl uninstall --yes
```

`lbctl upgrade` met à niveau les images des services LangBot. Les déploiements installés et adoptés utilisent les mêmes commandes :

```sh
lbctl upgrade --version <newer-vX.Y.Z> --dry-run -o json
lbctl upgrade --version <newer-vX.Y.Z> --yes -o json
```

La mise à niveau remplace les images de `langbot`, `langbot_plugin_runtime` et du `langbot_box` existant, tout en conservant ports, montages, environnement, commentaires et autres services. Elle télécharge et vérifie l'image, arrête le déploiement, sauvegarde les données, Compose, le `.env` du projet s'il existe et le registre du déploiement, puis démarre et vérifie la version. Les sauvegardes et l'état de récupération sont conservés après un échec et consultables avec `status` et `doctor`.

La mise à niveau automatique prend en charge les services déclarant directement `rockchin/langbot:<tag>`, dont toutes les données sont dans le montage `data` du répertoire Compose, avec SQLite, Chroma et local storage locaux. Configuration, périmètre de sauvegarde et versions sont vérifiés avant une mise à niveau vers une version supérieure. `--dry-run` affiche le plan ; l'exécution vérifie l'image. Après une modification manuelle de Compose, relancez `adopt` sur la même destination pour confirmer la nouvelle empreinte.

Le cycle de vie local cible l'instance déployée. Bot, Pipeline, Skill et les autres ressources utilisent les commandes communes ci-dessous pour gérer le Workspace du contexte.

## Gestion des ressources

Utilisez des fichiers JSON ou YAML pour les corps complexes, avec les champs de l'API HTTP LangBot. `--file -` lit stdin.

### Bot et Pipeline

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

`pipeline apply` met à jour si le corps contient `uuid`, et crée sinon.

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

Téléversez un document, lancez l'ingestion et attendez la fin :

```sh
lbctl knowledge-base ingest <uuid> --file ./document.pdf --wait
```

### Provider et Model

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

Les types Model sont `llm`, `embedding` et `rerank`. La sortie Provider/Model applique un masquage serveur et un filtrage client ; la découverte des modèles renvoie des résultats structurés.

### Plugin et Skill

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

L'installation Skill avec `--dry-run` utilise un aperçu serveur. L'installation, la mise à niveau et la suppression Plugin proposent `--wait` pour suivre les tâches asynchrones.

Les requêtes GitHub incluent `owner`, `repo`, `release_tag` et `release_id`/`asset_id` ou `asset_url`. Pour une URL, la CLI consulte la release et envoie les mêmes identifiants que WebUI. La consultation GitHub est séparée de l'authentification Workspace ; dry-run vérifie les prérequis. Exemple :

```yaml
owner: langbot-app
repo: langbot-plugins
release_tag: 0.0.1.alpha.1
asset_url: https://github.com/langbot-app/langbot-plugins/releases/download/0.0.1.alpha.1/langbot-team-GoogleSearch-0.1.2.lbpkg
```

Les mises à jour de configuration attendent, dans un délai borné, une relecture après rechargement. La suppression prend en charge les plugins actifs et les installations ayant échoué au démarrage.

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

## Écritures et tâches

Les écritures vérifient le contexte enregistré, le lien Workspace, l'identité réelle, les permissions, les capacités et les prérequis des ressources. Règles générales :

- `--dry-run` vérifie les prérequis ; l'installation Skill utilise un aperçu serveur.
- La suppression exige un `--yes` explicite.
- Les écritures vérifiables relisent la ressource après exécution.
- Un résultat incertain renvoie `result_unknown` pour examen avant la prochaine décision.
- Les opérations en plusieurs étapes conservent les ressources créées ou les `task_id` pour le suivi.

Les lectures de Bot/Pipeline conservent la configuration ordinaire et masquent les identifiants avec `***`. Les créations, mises à jour et copies de Pipeline comparent les champs observables après relecture ; les différences renvoient `verification_failed`. `verification.observable_fields_match` indique le résultat et `unverified_fields` liste les champs masqués. `verified` vaut true uniquement si tous les champs contrôlés sont vérifiables et identiques. Les mises à jour de Bot préservent les identifiants existants pour les valeurs exactement égales à `***` ; les valeurs impossibles à restaurer en toute sécurité sont refusées avant la mise à jour.

Consultez les tâches asynchrones indépendamment :

```sh
lbctl task list
lbctl task list --type <type> --kind <kind>
lbctl task get <task-id>
lbctl task get <task-id> --wait --wait-timeout 10m
```

L'attente locale et le cycle de vie serveur sont indépendants ; poursuivez le suivi avec `task get`.

## Accès API contrôlé

`api get/post` appelle les opérations inscrites au registre intégré :

```sh
lbctl api get '/api/v1/system/tasks?kind=plugin'
lbctl api post '/api/v1/knowledge/bases/<uuid>/retrieve' --file query.yaml
```

Utilisez les commandes dédiées pour la gestion Provider/Model, l'installation, les tests et les opérations sensibles. `raw` fournit une lecture compatible de `/api/v1/system/info`.

## Sortie et vérifications

La sortie par défaut est concise et lisible. Pour l'automatisation, `-o json` ou `-o yaml` produit des résultats structurés complets. Résultats et erreurs vont sur stdout, diagnostics sur stderr.

`status` résume l'environnement actif et conserve les avertissements d'identité et de récupération. Ajoutez `-o json` ou `-o yaml` à la même commande pour obtenir les identifiants complets d'instance/Workspace et les chemins de déploiement. Une liste de capacités obtenue ne signifie pas que toutes les opérations sont prises en charge ; vérifiez leurs états avec `capabilities`.

| Code de sortie | Signification |
|---|---|
| 0 | Succès |
| 2 | Erreur d'entrée ou de configuration |
| 3 / 4 / 5 | Erreur d'authentification / accès refusé / introuvable |
| 6 | Prérequis manquant ou conflit |
| 7 | Réseau, TLS, délai dépassé ou annulation |
| 8 | Incompatibilité API ou protocole |
| 9 | Récupération du déploiement requise |
| 10 | Erreur serveur ou interne non classée |

```sh
make check
make cross-build

# Read-only diagnostics against a running Core.
LBCTL_TEST_ENDPOINT=http://localhost:5300 \
  go test ./internal/integration -run TestLiveCore -v
```

`make cross-build` génère les binaires amd64/arm64 pour macOS, Linux et Windows, ainsi que `dist/checksums.txt`.

Découverte des commandes pour les agents et complétion shell :

```sh
lbctl schema
lbctl schema --command plugin.install.github
lbctl completion bash
```

## Découverte de configuration RAG

Ces commandes utilisent les capacités Core correspondantes et exigent `resource.view`. Elles proposent `-o json` et `-o yaml`.

```sh
lbctl knowledge-engine list
lbctl knowledge-engine creation-schema author/engine -o yaml
lbctl knowledge-engine retrieval-schema author/engine -o json
lbctl knowledge-parser list --mime-type application/pdf
```

Choisissez un `plugin_id` dans la liste des moteurs. Le creation schema définit la création de la base de connaissances, le retrieval schema les paramètres de recherche. Utilisez `knowledge-base create`, `ingest` et `retrieve` pour l'ingestion et la recherche.

## Liens d'extensions Pipeline

```sh
lbctl pipeline extensions get PIPELINE_UUID -o json
```

`pipeline extensions update PIPELINE_UUID --file extensions.yaml` **remplace tous les liens**. Les huit champs sont obligatoires. Inspectez les liens et ressources avec `extensions get` et conservez chaque élément souhaité. La mise à jour est vérifiée par relecture ; `--dry-run` contrôle les entrées et prérequis.

Cet exemple vide les quatre listes de liens explicites et désactive tous les commutateurs.

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

Des tableaux vides avec `enable_all_*: false` laissent ce type sans lien ; `true` active toutes ses extensions disponibles. Plugin utilise `{author: author, name: plugin}`, MCP Server le `uuid` de `available_mcp_servers`, Skill le `name` de `available_skills`. Exemple MCP Resource : `{server_uuid: SERVER_UUID, uri: "docs://guide"}`. `mcp_resource_agent_read_enabled` contrôle la liste et la lecture des ressources par l'Agent, ainsi que leur inclusion dans le contexte.

La mise à jour exige un contexte enregistré et lié au Workspace, `resource.manage`, `resource.view` et la capacité Core correspondante.

## Essai Pipeline en un tour

```sh
lbctl pipeline run PIPELINE_UUID --message 'Réponds à cette question avec la base de connaissances' --timeout 2m -o json
```

L'essai utilise un contexte enregistré et lié, avec `runtime.operate` et `resource.view`, et appelle les modèles et outils configurés. Chaque exécution crée une nouvelle session. Le résultat contient la réponse, l'ID de session, l'ID de requête et les IDs de message disponibles ; un échec renvoie un code non nul.

Core attend jusqu'à 60 secondes ; `--timeout` contrôle l'attente HTTP de la CLI. Un dépassement signifie **état d'exécution inconnu**. Consultez les historiques avec les IDs de message/session dans WebUI (`/home/monitoring`), ou recherchez les enregistrements récents par Pipeline et heure.

Core déclare `pipeline.run` ; `--dry-run` vérifie les prérequis.

## Diagnostic des historiques d'exécution

Consultez les historiques, détails des messages en échec et erreurs d'appels dans le Workspace de l'API Key courante, via les capacités Core correspondantes.

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

Les requêtes exigent `resource.view`. Les listes acceptent `--limit 1..500` et `--offset`, selon les limites Core. `--start-time` et `--end-time` utilisent ISO 8601. Voir les filtres dans `--help`.

## Diagnostic Sandbox en lecture seule

```sh
lbctl sandbox status
lbctl sandbox sessions
lbctl sandbox errors
```

Sandbox consulte le Workspace courant : l'état exige `resource.view`, les sessions et erreurs `audit.view`. Le sandbox géré utilise les contrôles d'admission serveur ; l'état contient `enabled`, `available` et les explications du serveur.

Core déclare les capacités correspondantes ; `-o json` et `-o yaml` sont pris en charge. `enabled: false` indique la désactivation ; `enabled: true` avec `available: false`, l'indisponibilité.
