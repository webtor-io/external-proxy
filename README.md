# external-proxy
Proxies external urls

`/<base64 url>/<name>` answers with the body of that http(s) or `data:` url.
Remote urls are fetched only from public unicast addresses: every connection,
redirects included, is checked after DNS resolution. An origin that lives on a
private network (a self-hosted instance's own `DOMAIN`, say) has to be listed in
`PRIVATE_NETWORK_ORIGINS`; a refused fetch logs the host, the reason and this
hint, and answers 404 like any other failure. Bodies are capped at 20 MB.

# Sample usage

```
% ./external-proxy --help
NAME:
   external-proxy - Proxies external urls

USAGE:
   external-proxy [global options] command [command options] [arguments...]

VERSION:
   0.0.1

COMMANDS:
   help, h  Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --probe-host value  probe listening host
   --probe-port value  probe listening port (default: 8081)
   --host value        listening host
   --port value        http listening port (default: 8080)
   --private-network-origins value  origins (scheme://host[:port]) fetched even when they resolve to a private, loopback or other non-public address, such as this instance's own web-ui; any other url is fetched from public addresses only [$PRIVATE_NETWORK_ORIGINS]
   --help, -h          show help
   --version, -v       print the version
```
