# dmn-status

Uptime-Dienst hinter [status.dmn-software.com](https://status.dmn-software.com). Prüft Dienste per
HTTP, TCP und FiveM-Abfrage, zeigt ihren Zustand auf einer öffentlichen Statusseite (Deutsch und
Englisch) und meldet Ausfälle per Mail.

- Öffentliche Seite mit Verfügbarkeit über 24 Stunden, 7 und 30 Tage, Latenz und Vorfällen
- Admin-Bereich mit Login zum Anlegen, Pausieren und Löschen von Zielen
- Mail bei Ausfall und Wiederkehr
- `/api/status.json` mit Zustand und Verfügbarkeit der öffentlichen Ziele
- `/metrics` im Prometheus-Format, nur mit Token
- Ein Go-Binary ohne Web-Framework, Templates und statische Dateien sind eingebettet
- SQLite über `modernc.org/sqlite`, kein CGO

## Checks

Jedes Ziel hat ein Intervall (30 bis 86400 Sekunden), ein Timeout (500 ms bis 60 s, höchstens
knapp unter dem Intervall) und eine Fehlerschwelle (1 bis 20). Erst wenn so viele Checks
nacheinander fehlschlagen, wird ein Vorfall eröffnet und eine Mail verschickt. Der nächste
erfolgreiche Check beendet den Vorfall.

| Art | Adresse | Geprüft wird |
|---|---|---|
| `http` | URL mit `http://` oder `https://` | GET-Anfrage, der Statuscode muss im erwarteten Bereich liegen (Standard `200-399`, auch Listen wie `200-299,401`). Optional muss ein Stichwort in den ersten 1 MiB der Antwort stehen. Weiterleitungen werden nicht verfolgt, TLS-Zertifikate werden geprüft. |
| `tcp` | `host:port` | Ob sich eine TCP-Verbindung aufbauen lässt. |
| `fivem` | `host:port` | `info.json` des FXServers muss gültiges JSON mit Serverversion liefern. `players.json` liefert zusätzlich die Spielerzahl, ist aber optional, weil viele Server sie sperren. Gespeichert wird nur die Anzahl, keine Namen oder Kennungen. |

Jeder Check baut eine neue Verbindung auf, die gemessene Latenz enthält also Verbindungsaufbau
und TLS. Bei `http` und `fivem` kann `connect_to` (`host:port`) die TCP-Verbindung an ein anderes
Ziel lenken, URL, SNI und Host-Header bleiben dabei unverändert. Damit lässt sich ein Dienst über
einen internen Reverse Proxy prüfen.

## Betrieb

Die Beispieldateien liegen in `deploy/`: ein mehrstufiges `Dockerfile` (statisches Binary in
`distroless/static`, läuft als `nonroot`), eine `docker-compose.yml` und `env.example`.

```sh
cp deploy/env.example deploy/.env
chmod 600 deploy/.env
# Werte anpassen, siehe Konfiguration

docker network create --subnet=172.20.0.0/24 status_edge
docker compose -f deploy/docker-compose.yml up -d --build
```

Die Compose-Datei veröffentlicht keinen Port. Sie erwartet einen Reverse Proxy mit TLS im extern
angelegten Netz `status_edge`, dessen Subnetz zu `TRUSTED_PROXY` passen muss.
`deploy/caddy-block.txt` ist ein Beispiel für Caddy. Der Container läuft mit schreibgeschütztem
Dateisystem, ohne Capabilities und mit `GOMEMLIMIT` unter dem Speicherlimit, die Datenbank liegt
im Volume unter `/data`.

### Erster Admin

Am sichersten über stdin, dann steht das Passwort in keiner Datei und keiner Container-Konfiguration:

```sh
docker compose -f deploy/docker-compose.yml exec -T dmn-status /dmn-status admin set-password <name>
```

Das Passwort braucht mindestens 12 Zeichen. `set-password` meldet alle bestehenden Sitzungen ab
und ist auch der Weg für jeden späteren Passwortwechsel.

Alternativ legt `serve` beim ersten Start einen Admin aus `ADMIN_USER` und `ADMIN_PASSWORD` an,
solange noch keiner in der Datenbank steht. Danach werden die Variablen ignoriert und gehören aus
der `.env` entfernt, gefolgt von `docker compose up -d --force-recreate`.

### Befehle

```
dmn-status serve                        Dienst starten (Standard im Container)
dmn-status admin set-password <name>    Admin anlegen oder Passwort setzen, Passwort über stdin
dmn-status healthcheck                  fragt /healthz auf 127.0.0.1 und dem Port aus ADDR ab
dmn-status backup <datei>               konsistente Kopie der Datenbank per VACUUM INTO
dmn-status version                      Version des Builds
```

`healthcheck` ist im Dockerfile als `HEALTHCHECK` eingetragen und braucht keine Shell im Image.
`backup` läuft auch neben dem laufenden Dienst, die Zieldatei darf noch nicht existieren:

```sh
docker compose -f deploy/docker-compose.yml exec dmn-status /dmn-status backup /data/backup.db
```

## Konfiguration

Alle Einstellungen kommen aus Umgebungsvariablen. Ungültige Werte brechen den Start mit einer
Meldung je Variable ab.

| Variable | Standard | Format und Grenzen |
|---|---|---|
| `ADDR` | `:8080` | Listen-Adresse als `host:port` |
| `DB_PATH` | `data/status.db` | Pfad der SQLite-Datei, im Container `/data/status.db` |
| `BASE_URL` | leer | z. B. `https://status.example.com`, nur Schema `http`/`https` und Host, ohne Pfad, Query oder Zugangsdaten. Wird für Links in Mails genutzt. Die daraus aufgelösten öffentlichen IPs sind für Checks gesperrt, damit der Dienst sich nicht über den eigenen Proxy selbst prüft. Der Host sollte deshalb nicht hinter einem CDN oder fremden Proxy liegen, sonst wird dessen gemeinsam genutzte IP gesperrt statt der eigenen Origin-IP. |
| `TRUSTED_PROXY` | leer | CIDR des Proxy-Netzes, z. B. `172.20.0.0/24`. Nur von Adressen aus diesem Netz wird `X-Forwarded-For` ausgewertet (letzter Eintrag). Leer: `X-Forwarded-For` wird ignoriert. |
| `COOKIE_SECURE` | `true` | `true` oder `false`. Mit `true` tragen die Cookies das Präfix `__Host-` und funktionieren nur über HTTPS. `false` nur für lokale Tests ohne TLS. |
| `PRIVATE_ALLOW` | leer | Kommagetrennte `host:port`-Liste, Port 1 bis 65535, z. B. `caddy:443`. Gilt nur für `connect_to`, nicht für die Adresse eines Ziels. |
| `RETENTION_DAYS` | `30` | 1 bis 400. So lange bleiben einzelne Check-Ergebnisse. Stundenwerte und beendete Vorfälle bleiben 400 Tage, offene Vorfälle immer. |
| `METRICS_TOKEN` | leer | Leer schaltet `/metrics` ab, sonst mindestens 32 Zeichen. Abruf mit `Authorization: Bearer <token>`. |
| `SMTP_HOST` | leer | SMTP-Server, z. B. `smtp.example.com`. Pflicht, sobald `MAIL_TO` gesetzt ist. |
| `SMTP_PORT` | `465` | 1 bis 65535. Nur implizites TLS, kein STARTTLS und kein Klartext. |
| `SMTP_USER` | leer | Nur zusammen mit `SMTP_PASS` |
| `SMTP_PASS` | leer | Nur zusammen mit `SMTP_USER`. Wird nicht getrimmt. |
| `MAIL_FROM` | leer | Absenderadresse, Pflicht bei aktivem Mailversand |
| `MAIL_TO` | leer | Empfänger als Adressliste, z. B. `a@example.com, b@example.com`. Anzeigenamen werden verworfen. |
| `ADMIN_USER` | leer | Nur für die Erstinstallation, nur zusammen mit `ADMIN_PASSWORD`. Bis 60 Zeichen, keine Leerzeichen. |
| `ADMIN_PASSWORD` | leer | Nur für die Erstinstallation, mindestens 12 Zeichen. Wird nicht getrimmt. |

Mails werden nur verschickt, wenn `SMTP_HOST` und `MAIL_TO` gesetzt sind. Ohne sie läuft der
Dienst normal weiter und schreibt beim Start eine Warnung ins Log.

Die früheren Variablen `TRUST_PROXY` und `ALLOW_PRIVATE` gibt es nicht mehr. Sind sie noch
gesetzt, startet der Dienst nicht, damit eine alte `.env` nicht still anders wirkt.

## Entwicklung

Voraussetzung ist Go 1.27.

```sh
go test -count=1 ./...
golangci-lint run
```

Die Oberfläche lässt sich mit Beispieldaten ohne Datenbank ansehen:

```sh
go run -tags dev ./cmd/preview
```

Die Vorschau lauscht auf `127.0.0.1:8081`. Die CI prüft zusätzlich `gofmt`, `go vet`,
`go test -race`, `govulncheck` und den Docker-Build.

## Sicherheit

Das Admin-Passwort wird mit argon2id gehasht (19 MiB, 2 Durchläufe). Login-Versuche sind je IP und
insgesamt begrenzt, Formulare im Admin sind mit CSRF-Token geschützt, zusätzlich zur
Cross-Origin-Prüfung von `net/http`. Die Content-Security-Policy erlaubt nur Skripte und Styles
vom eigenen Ursprung, ohne Inline-Code. Checks prüfen die Zieladresse erst nach der
Namensauflösung: Loopback, private, Link-Local- und reservierte Netze sind gesperrt, private
Ziele sind nur über `connect_to` und `PRIVATE_ALLOW` erreichbar. Die öffentliche Seite zeigt
weder Adressen noch Fehlertexte.

Sicherheitslücken bitte nicht als öffentliches Issue melden, sondern wie in
[SECURITY.md](SECURITY.md) beschrieben.

## Lizenz

Alle Rechte vorbehalten, siehe [LICENSE](LICENSE).
