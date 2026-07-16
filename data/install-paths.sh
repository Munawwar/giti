APP_ID=io.github.Munawwar.Giti
if [ "$USER_INSTALL" = true ]; then
    DATA_HOME=${XDG_DATA_HOME:-${HOME}/.local/share}
    FISH_COMPLETION_HOME=${XDG_CONFIG_HOME:-${HOME}/.config}/fish/completions
else
    DATA_HOME=$PREFIX/share
    FISH_COMPLETION_HOME=$DATA_HOME/fish/vendor_completions.d
fi
BASH_COMPLETION_HOME=$DATA_HOME/bash-completion/completions
ZSH_COMPLETION_HOME=$DATA_HOME/zsh/site-functions
