# Sicherheitslücken melden

## Unterstützte Versionen

Es gibt keine Releases mit eigener Pflege. Korrigiert wird nur der aktuelle Stand des Branches
`main`, der auch auf status.dmn-software.com läuft.

## Meldeweg

Bitte keine öffentlichen Issues, Pull Requests oder Diskussionen zu Sicherheitslücken. Gemeldet
wird vertraulich über GitHub Private Vulnerability Reporting: im Repository den Tab **Security**
öffnen und dort **Report a vulnerability** wählen.

Hilfreich sind:

- betroffene Stelle (Route, Befehl, Datei oder Umgebungsvariable)
- Schritte zum Nachvollziehen und die Auswirkung
- die Version bzw. der Commit, gegen den getestet wurde

Bitte nur gegen eine eigene Installation testen, nicht gegen status.dmn-software.com oder die
dort geprüften Dienste.

## Was danach passiert

Jede Meldung wird gelesen und über denselben Weg beantwortet. Das Projekt wird von einer Person
betreut, feste Antwort- oder Behebungszeiten gibt es deshalb nicht. Bestätigte Lücken werden
in `main` behoben und danach über ein GitHub Security Advisory veröffentlicht. Wer die Lücke
gemeldet hat, wird dort auf Wunsch genannt.
