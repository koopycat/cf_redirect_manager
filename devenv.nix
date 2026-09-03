{ pkgs, ... }:

{
  dotenv.disableHint = true;

  packages = with pkgs; [
    git
    go_1_25
    just
  ];

  scripts.check.exec = "just check";
  scripts.build.exec = "just build";
  scripts.test.exec = "just test";
  scripts.race.exec = "just race";
  scripts.fmt.exec = "just fmt";
}
