# Purpur 26.2, the counterpart of nix/paper-26.2.nix, and deleted with it. It
# takes that file's Mojang jar. The build is nix/purpur.nix's.
{ fetchurl
, jdk25_headless
, stdenvNoCC
, mojangJar
}:

rec {
  purpurVersion = "26.2";
  purpurBuild = "2628";

  purpurJar = fetchurl {
    url = "https://api.purpurmc.org/v2/purpur/${purpurVersion}/${purpurBuild}/download";
    hash = "sha256-dbnEn/0J8mGA+0qyhdhA2oBvebNH8v4iVq3iaR2hVJI=";
  };

  repo = stdenvNoCC.mkDerivation {
    pname = "purpur-repo";
    version = "${purpurVersion}+${purpurBuild}";

    dontUnpack = true;
    nativeBuildInputs = [ jdk25_headless ];

    buildPhase = ''
      runHook preBuild

      mkdir -p work/cache
      cp ${mojangJar} work/cache/mojang_${purpurVersion}.jar
      cd work
      java -Dpaperclip.patchonly=true -DbundlerRepoDir=. -jar ${purpurJar}
      cd ..

      runHook postBuild
    '';

    installPhase = ''
      runHook preInstall

      mkdir -p $out
      cp -r work/versions work/libraries work/cache $out/
      chmod -R a-w $out

      runHook postInstall
    '';

    meta.description = "Purpur ${purpurVersion} build ${purpurBuild}, patched at build time";
  };
}
