# Cache-Tests

Setup https://github.com/http-tests/cache-tests

1) run the server `npm run server`
2) run the tests `NODE_TLS_REJECT_UNAUTHORIZED=0 npm run cli --base=http://localhost --silent > results/caddy-cache-handler.json`
3) to check the results you may want to add this to the `results/index.mjs`:

```
  {
    file: 'caddy-cache-handler.json',
    name: 'Caddy',
    type: 'rev-proxy',
    version: 'dev'
  }
```

4) Open https://localhost
