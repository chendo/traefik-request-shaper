# Traefik Request Shaper

Goal: Mitigate DDOS attacks while minimising experience of real users

This plugin improves on the built-in rate limit functionality by enabling delaying processing of the request rather than immediately returning a 429 to the client. Inspired `nginx`s rate limiting mechanism.

The downside of regular rate limiting that immediately returns an error is you tend to need to have to be more lax with your limits so you don't inadvertently affect real users. Delaying requests rather than terminating them allows you to set stricter limits while minimising errors to real users.

Note that the rate limiting mechanism is in memory and is not shared between traefik instances.

[![Build Status](https://github.com/chendo/traefik-request-shaper/workflows/Main/badge.svg?branch=main)](https://github.com/chendo/traefik-request-shaper/actions)

## Status

Alpha. Use at your own risk.

## Configuration

```
      - traefik.http.middlewares.request-shaper.plugin.traefik-request-shaper.average=10
      - traefik.http.middlewares.request-shaper.plugin.traefik-request-shaper.period=1s
      - traefik.http.middlewares.request-shaper.plugin.traefik-request-shaper.maxDelay=1s
      - traefik.http.middlewares.request-shaper.plugin.traefik-request-shaper.burst=0
      - traefik.http.middlewares.request-shaper.plugin.traefik-request-shaper.exceedWait=2s
      - traefik.http.middlewares.request-shaper.plugin.traefik-request-shaper.ttl=1m
```

```yaml
# Static configuration

experimental:
  plugins:
    example:
      moduleName: github.com/chendo/traefik-request-shaper
      version: v0.1.0
```
## Contributing

Pull requests welcome.