{
  description = "shared - self-hosted static site platform";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (pkgs: rec {
        shared = pkgs.buildGoModule {
          pname = "shared";
          version = "0.1.0";
          src = self;
          vendorHash = "sha256-afwv9acPqI5YJJGdKJSiRzcVYFl+cAIwiQFxhbLQ3cI=";
          subPackages = [ "cmd/sharedd" "cmd/shared" ];
          ldflags = [ "-s" "-w" ];
        };
        default = shared;
      });

      nixosModules.default = { config, lib, pkgs, ... }:
        let
          cfg = config.services.shared;
        in
        {
          options.services.shared = {
            enable = lib.mkEnableOption "shared, the self-hosted static site platform";

            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
              description = "The shared package to run.";
            };

            port = lib.mkOption {
              type = lib.types.port;
              default = 8787;
              description = "Port sharedd listens on.";
            };

            baseHost = lib.mkOption {
              type = lib.types.str;
              default = "localhost";
              example = "shared.tap";
              description = "Base host for subdomain routing; sites live at <name>.<baseHost>.";
            };

            urlMode = lib.mkOption {
              type = lib.types.enum [ "subdomain" "path" ];
              default = "subdomain";
              description = ''
                How sites are addressed: `subdomain` gives each site its own host
                (`<name>.<baseHost>`), `path` puts every site under one prefix
                (`<baseHost><pathPrefix>/<name>/`) for hosts without wildcard DNS.
              '';
            };

            pathPrefix = lib.mkOption {
              type = lib.types.str;
              default = "/s";
              example = "/sites";
              description = "Path-mode prefix; sites live at <pathPrefix>/<name>/.";
            };

            publicUrl = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
              example = "https://shared.example.com";
              description = ''
                Origin clients actually use, when a reverse proxy (e.g.
                `tailscale serve`) terminates TLS in front of sharedd. Scheme and
                host[:port] only, no path. Every URL the server states is built
                from it instead of from the listen address. Its host must match
                {option}`baseHost`.
              '';
            };

            aiModel = lib.mkOption {
              type = lib.types.str;
              default = "claude-opus-4-8";
              description = "Default model for /api/ai/chat.";
            };

            environmentFile = lib.mkOption {
              type = lib.types.nullOr lib.types.path;
              default = null;
              example = "/run/secrets/shared.env";
              description = "Optional EnvironmentFile, e.g. for OPENAI_BASE_URL + OPENAI_API_KEY.";
            };

            openFirewall = lib.mkOption {
              type = lib.types.bool;
              default = false;
              description = "Open the listen port in the firewall.";
            };
          };

          config = lib.mkIf cfg.enable {
            systemd.services.shared = {
              description = "shared site platform";
              after = [ "network-online.target" ];
              wants = [ "network-online.target" ];
              wantedBy = [ "multi-user.target" ];

              environment = {
                SHARED_ADDR = ":${toString cfg.port}";
                SHARED_DATA = "/var/lib/shared";
                SHARED_BASE_HOST = cfg.baseHost;
                SHARED_URL_MODE = cfg.urlMode;
                SHARED_PATH_PREFIX = cfg.pathPrefix;
                SHARED_AI_MODEL = cfg.aiModel;
              } // lib.optionalAttrs (cfg.publicUrl != null) {
                SHARED_PUBLIC_URL = cfg.publicUrl;
              };

              serviceConfig = {
                ExecStart = "${cfg.package}/bin/sharedd";
                DynamicUser = true;
                StateDirectory = "shared";
                WorkingDirectory = "/var/lib/shared";
                Restart = "on-failure";
                RestartSec = 2;
                EnvironmentFile = lib.optional (cfg.environmentFile != null) cfg.environmentFile;

                AmbientCapabilities = lib.mkIf (cfg.port < 1024) [ "CAP_NET_BIND_SERVICE" ];
                NoNewPrivileges = true;
                ProtectSystem = "strict";
                ProtectHome = true;
                PrivateTmp = true;
                PrivateDevices = true;
                ProtectKernelTunables = true;
                ProtectControlGroups = true;
                RestrictAddressFamilies = [ "AF_INET" "AF_INET6" ];
              };
            };

            networking.firewall.allowedTCPPorts = lib.mkIf cfg.openFirewall [ cfg.port ];
          };
        };

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [ go gopls ];
        };
      });
    };
}
