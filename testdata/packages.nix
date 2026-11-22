{ pkgs, ... }:

let
  cliAndCore = with pkgs; [
    vim wget curl git gh
    eza bat btop fastfetch ripgrep fzf
    unzip p7zip zellij tmux
    wl-clipboard killall jq tree
    htop ncdu
    just direnv
    neovim
    lazygit
  ];

  development = with pkgs; [
    python3 python3Packages.pip nodejs_22 yarn pnpm
    go
    gnumake llvmPackages.clang
    openjdk gradle kotlin
    rustup pkg-config
    (python3.withPackages (ps: [ ps.requests ]))
    cmake
  ];

  desktopApps = with pkgs; [
    (firefox.override {
      extraPolicies = {
        DisableTelemetry = true;
        DontCheckDefaultBrowser = true;
      };
    })
    chromium
    vscode
    thunderbird
    vlc mpv
    libreoffice-qt okular
    keepassxc
    inkscape
    audacity
    transmission_4-gtk
    ghostty
  ];

  systemAndVm = with pkgs; [
    gnome-tweaks
    gnome-boxes
    resources
    cifs-utils ntfs3g
    podman-compose
    ffmpeg
    imagemagick
  ];

  compatibility = with pkgs; [
    wineWow64Packages.stable winetricks
    protontricks
  ];

  mediaAndGraphics = [
    (pkgs.blender.overrideAttrs (oldAttrs: {
      postFixup = (oldAttrs.postFixup or "") + ''
        wrapProgram $out/bin/blender \
          --set QT_QPA_PLATFORM xcb \
          --set GDK_BACKEND x11
      '';
    }))
  ];
in
{
  environment.systemPackages =
    cliAndCore
    ++ development
    ++ desktopApps
    ++ systemAndVm
    ++ compatibility
    ++ mediaAndGraphics;
}
