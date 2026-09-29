# The pinned Velocity artifact.
#
# Unlike Paper, this needs no build-time patching: Velocity ships as a fat jar
# with nothing to download on first start. The hash was computed from a
# download and checked in here; that does not make the source trustworthy, it
# makes the artifact frozen — a changed upstream breaks the build instead of
# substituting a jar quietly.
#
# The 4.x line, because 3.x never learned Minecraft 26.3 (protocol 777); 4.2.0
# is the first release that did. Its classes are class-file major 69, so the
# agents' tests need a Java 25 JVM -- see nix/agents.nix.
{ fetchurl }:

rec {
  velocityVersion = "4.2.0";
  velocityBuild = "30";

  jar = fetchurl {
    url = "https://fill-data.papermc.io/v1/objects/35a5596a5468a035d8a32c8de5ebb0dc6b8d8f0cc3ff5169d514aca762af8aa8/velocity-${velocityVersion}-${velocityBuild}.jar";
    hash = "sha256-NaVZalRooDXYoyyN5euw3GuNjwzD/1Fp1RSsp2Kviqg=";
  };

  # config-version = "2.9", measured out of the pinned jar above with:
  #
  #   JAR=$(nix build .#velocity-jar --no-link --print-out-paths)
  #   jar xf "$JAR" default-velocity.toml && cat default-velocity.toml
  #
  # (default-velocity.toml sits at the jar root, not under META-INF; `unzip`
  # is not on PATH in the dev shell, so `jar xf` extracts it instead.)
  #
  # This is what velocityBuild 30 validates and migrates a rendered
  # velocity.toml against. Task 5 writes this exact value into the rendered
  # file; a version bump that does not re-run the command above and update
  # this comment produces a config Velocity migrates out from under the
  # renderer on first start.
  #
  # That same extracted file is checked in as
  # internal/render/defaults/velocity.default.toml, which is what
  # TestVelocityWritesTheKeysVelocityItselfReads compares the renderer's key
  # names against and what TestVelocityWritesThePinnedConfigVersion compares
  # this version to. A bump has to refresh it in the same change.
}
