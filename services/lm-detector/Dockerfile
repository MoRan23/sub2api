FROM oven/bun:1.4.2
WORKDIR /app
COPY --chown=bun:bun package.json upstream.json detector.ts server.ts ./
COPY --chown=bun:bun vendor ./vendor
USER bun
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s CMD bun -e 'const r = await fetch("http://127.0.0.1:8080/healthz"); process.exit(r.ok ? 0 : 1)'
CMD ["bun", "run", "server.ts"]
