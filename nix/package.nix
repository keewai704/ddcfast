{ lib, buildGoModule ? null, buildGo126Module ? null, pkg-config, ddcutil, version ? "unstable" }:

let
  goBuilder =
    if buildGo126Module != null then buildGo126Module
    else if buildGoModule != null then buildGoModule
    else throw "buildGoModule is required";
in
goBuilder {
  pname = "ddcfast";
  inherit version;
  src = lib.cleanSource ../.;

  vendorHash = null;

  nativeBuildInputs = [ pkg-config ];
  buildInputs = [ ddcutil ];

  ldflags = [
    "-s"
    "-w"
  ];

  postInstall = ''
    install -Dm644 examples/config.json "$out/share/ddcfast/config.json"
    install -Dm644 systemd/ddcfast.service "$out/lib/systemd/user/ddcfast.service"
  '';

  meta = with lib; {
    description = "Fast DDC/CI monitor control daemon and CLI";
    homepage = "https://github.com/keewai704/ddcfast";
    license = licenses.mit;
    platforms = platforms.linux;
    mainProgram = "ddcfast";
  };
}
